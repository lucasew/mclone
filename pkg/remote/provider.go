package remote

import (
	"context"

	"github.com/lucasew/mclone/pkg/message"
)

// Model represents an AI model offered by a provider.
// It maps the provider's internal identifier (Slug) to a human-readable name.
type Model struct {
	Name string // display name
	Slug string // identifier for --model
}

// Provider defines the contract for an AI backend (e.g., OpenAI, Anthropic, local).
// Providers are responsible for listing their available models and handling
// chat completions. They are registered dynamically and resolved via the remote registry.
type Provider interface {
	// Name returns the provider's display name or type identifier.
	Name() string

	// List queries the backend for its currently available models.
	// This may involve network requests or authentication checks.
	List(ctx context.Context) ([]Model, error)

	// Chat streams a conversation completion from the backend.
	// It returns a channel that yields response chunks and an error if the request fails.
	// The caller is responsible for consuming the channel until it's closed.
	Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error)
}
