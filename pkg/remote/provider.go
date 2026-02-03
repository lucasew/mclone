package remote

import (
	"context"
	"io"
)

type Model struct {
	Name string
	Size int64
	ID   string
}

type ChatMessage struct {
	Role    string
	Content string
}

type ChatRequest struct {
	Model    string
	Messages []ChatMessage
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
	Chat(ctx context.Context, req ChatRequest) (<-chan ChatResponse, error)
}
