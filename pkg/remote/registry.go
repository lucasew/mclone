package remote

import (
	"fmt"
)

type Factory func(name string, options map[string]string) (Provider, error)

var registry = make(map[string]Factory)

func Register(typeName string, factory Factory) {
	registry[typeName] = factory
}

func NewProvider(typeName string, name string, options map[string]string) (Provider, error) {
	factory, ok := registry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown provider type: %s", typeName)
	}
	return factory(name, options)
}
