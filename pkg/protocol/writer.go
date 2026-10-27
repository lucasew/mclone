package protocol

import (
	"context"
	"net/http"

	"github.com/lucasew/mclone/pkg/message"
)

type Writer interface {
	ServeResponse(ctx context.Context, w http.ResponseWriter, ch <-chan message.Event, model string, stream bool)
}
