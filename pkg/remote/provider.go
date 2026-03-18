package remote

import (
	"context"

	"github.com/lucasew/mclone/pkg/message"
)

type Model struct {
	Name string // display name
	Slug string // identifier for --model
}

type Provider interface {
	Name() string
	List(ctx context.Context) ([]Model, error)
	Chat(ctx context.Context, req message.Request) (<-chan message.Event, error)
}
