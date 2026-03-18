package protocol

import (
	"net/http"

	"github.com/lucasew/mclone/pkg/message"
)

type Writer interface {
	ServeResponse(w http.ResponseWriter, ch <-chan message.Event, model string, stream bool)
}
