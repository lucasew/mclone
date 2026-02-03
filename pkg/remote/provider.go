package remote

import (
	"context"
	"io"

	"github.com/tmc/langchaingo/llms"
)

type Model struct {
	Name string
	Size int64
	ID   string
}

type ChatResponse struct {
	Content string
	Done    bool
	Error   error
}

type Provider interface {
	Name() string
	List(ctx context.Context) ([]Model, error)
	Get(ctx context.Context, name string) (io.ReadCloser, int64, error)
	Put(ctx context.Context, name string, size int64, data io.Reader) error
	Chat(ctx context.Context, modelName string, messages []llms.MessageContent) (<-chan ChatResponse, error)
}
