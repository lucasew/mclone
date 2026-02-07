package remote

import (
	"context"
	"io"

	"github.com/lucasew/mclone/pkg/message"
)

type Model struct {
	Name string
	Size int64
	ID   string
}

type Provider interface {
	Name() string
	List(ctx context.Context) ([]Model, error)
	Get(ctx context.Context, name string) (io.ReadCloser, int64, error)
	Put(ctx context.Context, name string, size int64, data io.Reader) error
	Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error)
}
