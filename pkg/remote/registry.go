package remote

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
)

type Factory func(name string, options map[string]string, resolve Resolver) (Provider, error)

// Resolver provides access to both provider and searcher resolution.
type Resolver struct {
	Provider func(remoteName string) (Provider, error)
	Searcher func(remoteName string) (Searcher, error)
}

var registry = make(map[string]Factory)

func Register(typeName string, factory Factory) {
	registry[typeName] = factory
}

func ListTypes() []string {
	var types []string
	for t := range registry {
		types = append(types, t)
	}
	return types
}

// NewResolver creates a Resolver from a config.
// It supports implicit balance groups via the ":" naming convention:
// remotes named "anth:1", "anth:2" form an implicit balance group "anth".
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

	return resolve
}

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
	groupOpts := map[string]string{}
	if exactMatch {
		groupOpts = rc.Options
	}

	factory, ok := registry["balance"]
	if !ok {
		return nil, fmt.Errorf("balance provider not registered")
	}

	opts := make(map[string]string)
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

func NewProvider(typeName string, name string, options map[string]string) (Provider, error) {
	factory, ok := registry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown provider type: %s", typeName)
	}
	return factory(name, options, Resolver{})
}
