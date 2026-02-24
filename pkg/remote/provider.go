package remote

import (
	"context"

	"github.com/lucasew/mclone/pkg/message"
)

// Model represents a language model available from a provider.
type Model struct {
	Name string // Display name shown to users
	Slug string // Unique identifier used in CLI arguments (e.g. --model)
}

// Provider defines the interface for interacting with LLM backends.
// Implementations handle the specifics of communicating with services like OpenAI, Anthropic, or Ollama.
type Provider interface {
	// Name returns the unique identifier of the provider instance.
	Name() string

	// List returns the available models for this provider.
	List(ctx context.Context) ([]Model, error)

	// Chat initiates a conversation with the specified model.
	// It returns a channel that streams ChatResponse chunks as they are generated.
	// The channel is closed when the response is complete.
	Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error)
}
