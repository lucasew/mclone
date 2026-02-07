package remote

import (
	"fmt"

	"github.com/lucasew/mclone/pkg/config"
)

type Factory func(name string, options map[string]string, resolve Resolver) (Provider, error)

// Resolver creates a provider from a remote name using the config.
type Resolver func(remoteName string) (Provider, error)

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
func NewResolver(conf *config.Config) Resolver {
	cache := make(map[string]Provider)
	var resolve Resolver
	resolve = func(remoteName string) (Provider, error) {
		if p, ok := cache[remoteName]; ok {
			return p, nil
		}
		rc, ok := conf.Remotes[remoteName]
		if !ok {
			return nil, fmt.Errorf("remote %q not found", remoteName)
		}
		factory, ok := registry[rc.Type]
		if !ok {
			return nil, fmt.Errorf("unknown provider type: %s", rc.Type)
		}
		p, err := factory(remoteName, rc.Options, resolve)
		if err != nil {
			return nil, err
		}
		cache[remoteName] = p
		return p, nil
	}
	return resolve
}

func NewProvider(typeName string, name string, options map[string]string) (Provider, error) {
	factory, ok := registry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown provider type: %s", typeName)
	}
	return factory(name, options, nil)
}
