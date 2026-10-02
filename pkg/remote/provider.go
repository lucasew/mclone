package remote

import (
	"context"

	"github.com/lucasew/mclone/pkg/message"
)

type Model struct {
	Name    string   // display name
	Slug    string   // identifier for --model
	OwnedBy []string // backend/provider labels used by /v1/models
}

type Provider interface {
	Name() string
	List(ctx context.Context) ([]Model, error)
	Chat(ctx context.Context, req message.Request) (<-chan message.Event, error)
}
