package tools

import (
	"context"
	"fmt"

	json "github.com/goccy/go-json"

	"github.com/lucasew/mclone/pkg/message"
)

// Tool is a single callable tool with its definition and executor.
type Tool struct {
	Definition message.ToolDefinition
	Execute    func(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolSource produces one or more tools.
type ToolSource interface {
	Tools(ctx context.Context) ([]Tool, error)
}

// ToolSourceFactory creates a ToolSource from config options.
type ToolSourceFactory func(name string, options map[string]any) (ToolSource, error)

var registry = make(map[string]ToolSourceFactory)

// Register registers a tool source type.
func Register(typeName string, factory ToolSourceFactory) {
	registry[typeName] = factory
}

// New creates a ToolSource by type name.
func New(typeName, name string, options map[string]any) (ToolSource, error) {
	factory, ok := registry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown tool source type: %s", typeName)
	}
	return factory(name, options)
}
