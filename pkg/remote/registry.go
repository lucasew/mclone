package remote

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/mitchellh/mapstructure"
)

// Factory is a function type for creating new Provider instances.
// It receives the name of the remote, its configuration options, and a Resolver for looking up other remotes.
type Factory func(name string, options map[string]any, resolve Resolver) (Provider, error)

// Resolver acts as a central registry for looking up providers and searchers.
// It abstracts away the details of configuration and instantiation.
type Resolver struct {
	// Provider resolves a remote name to a Provider instance.
	// It handles caching and implicit balance group resolution.
	Provider func(remoteName string) (Provider, error)

	// Searcher resolves a remote name to a Searcher instance.
	Searcher func(remoteName string) (Searcher, error)

	// UpdateOptions updates the configuration options for a given remote.
	// This persists the changes to the configuration file.
	UpdateOptions func(remoteName string, options map[string]any) error
}

var registry = make(map[string]Factory)

// Register adds a new provider type to the registry.
// The typeName is used in the configuration file to specify the provider type (e.g., "openai", "anthropic").
func Register(typeName string, factory Factory) {
	registry[typeName] = factory
}

// ListTypes returns a slice of all registered provider type names.
func ListTypes() []string {
	var types []string
	for t := range registry {
		types = append(types, t)
	}
	return types
}

// NewResolver creates a new Resolver instance based on the provided configuration.
//
// Implicit Balancing:
// NewResolver implements an implicit balancing mechanism. If a remote name is not explicitly defined
// with a type, the resolver looks for remotes that share the name as a prefix followed by a colon (e.g., "myremote:1", "myremote:2").
// If found, these remotes are automatically grouped into a balanced pool under the "myremote" name.
//
// Failover Configuration:
// When an implicit balance group is created, it inherits options from the base remote configuration if it exists.
// Additionally, it aggregates `failover_threshold` settings from individual members, allowing specific failover policies per backend.
//
// Configuration Updates:
// The returned Resolver includes an UpdateOptions function that writes changes back to the configuration file,
// ensuring persistence of runtime changes like disabling a failing backend.
func NewResolver(conf *config.Config) Resolver {
	providerCache := make(map[string]Provider)
	searcherCache := make(map[string]Searcher)

	var resolve Resolver

	resolve.Provider = func(remoteName string) (Provider, error) {
		if p, ok := providerCache[remoteName]; ok {
			return p, nil
		}

		p, err := resolveOne(conf, remoteName, resolve)
		if err != nil {
			return nil, err
		}
		providerCache[remoteName] = p
		return p, nil
	}

	resolve.Searcher = func(remoteName string) (Searcher, error) {
		if s, ok := searcherCache[remoteName]; ok {
			return s, nil
		}

		rc, ok := conf.Remotes[remoteName]
		if !ok {
			return nil, fmt.Errorf("search remote %q not found", remoteName)
		}
		factory, ok := searchRegistry[rc.Type]
		if !ok {
			return nil, fmt.Errorf("unknown search type: %s", rc.Type)
		}
		s, err := factory(remoteName, rc.Options)
		if err != nil {
			return nil, err
		}
		searcherCache[remoteName] = s
		return s, nil
	}

	resolve.UpdateOptions = func(remoteName string, options map[string]any) error {
		// Reload config to avoid overwriting concurrent changes (though mclone is mostly single process)
		c, err := config.LoadConfig()
		if err != nil {
			return err
		}
		rc, ok := c.Remotes[remoteName]
		if !ok {
			return fmt.Errorf("remote %q not found during update", remoteName)
		}

		slog.Info("updating_options", "remote", remoteName, "keys", len(options))

		// Merge options
		if rc.Options == nil {
			rc.Options = make(map[string]any)
		}
		for k, v := range options {
			rc.Options[k] = v
		}
		c.Remotes[remoteName] = rc

		// Update local conf copy too if needed, but for now just save to disk
		return c.Save()
	}

	return resolve
}

// resolveOne attempts to resolve a single provider or construct a balance group.
//
// It follows this logic:
// 1. Checks for an exact match in the config with a defined Type.
// 2. If no exact match with Type, looks for "implicit members" (remotes starting with "remoteName:").
// 3. If members are found, constructs a "balance" provider grouping them.
// 4. Propagates `failover_threshold` from members to the group options.
func resolveOne(conf *config.Config, remoteName string, resolve Resolver) (Provider, error) {
	rc, exactMatch := conf.Remotes[remoteName]

	// Exact match with a type → normal remote
	if exactMatch && rc.Type != "" {
		factory, ok := registry[rc.Type]
		if !ok {
			return nil, fmt.Errorf("unknown provider type: %s", rc.Type)
		}
		return factory(remoteName, rc.Options, resolve)
	}

	// Try implicit balance: find all "remoteName:*" entries
	prefix := remoteName + ":"
	var members []string
	for name, rc := range conf.Remotes {
		if strings.HasPrefix(name, prefix) && rc.Type != "" {
			members = append(members, name)
		}
	}
	sort.Strings(members)

	if len(members) == 0 {
		if exactMatch {
			return nil, fmt.Errorf("remote %q has no type and no %s:* members", remoteName, remoteName)
		}
		return nil, fmt.Errorf("remote %q not found", remoteName)
	}

	if len(members) == 1 {
		return resolve.Provider(members[0])
	}

	// Build implicit balance group
	groupOpts := map[string]any{}
	if exactMatch {
		groupOpts = rc.Options
	}

	factory, ok := registry["balance"]
	if !ok {
		return nil, fmt.Errorf("balance provider not registered")
	}

	opts := make(map[string]any)
	for k, v := range groupOpts {
		opts[k] = v
	}
	opts["remotes"] = strings.Join(members, ",")

	for _, m := range members {
		if mc, ok := conf.Remotes[m]; ok && mc.Options != nil {
			if ft, ok := mc.Options["failover_threshold"]; ok {
				opts["failover_threshold:"+m] = ft
			}
		}
	}

	return factory(remoteName, opts, resolve)
}

// NewProvider creates a new provider instance directly by type name.
// This bypasses the Resolver logic and is useful for direct instantiation or testing.
func NewProvider(typeName string, name string, options map[string]any) (Provider, error) {
	factory, ok := registry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown provider type: %s", typeName)
	}
	return factory(name, options, Resolver{})
}

// DecodeOptions maps a generic map[string]any configuration to a specific struct.
//
// It uses `mapstructure` with `WeaklyTypedInput: true`, enabling loose type conversion
// (e.g., parsing string "123" into an int field). This is critical for parsing TOML/env configurations
// where types might not strict matches.
func DecodeOptions(input interface{}, output interface{}) error {
	config := &mapstructure.DecoderConfig{
		Metadata:         nil,
		Result:           output,
		WeaklyTypedInput: true,
		TagName:          "mapstructure",
	}

	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return err
	}

	return decoder.Decode(input)
}
