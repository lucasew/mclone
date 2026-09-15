package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/protocol"
	anthropicprotocol "github.com/lucasew/mclone/pkg/protocol/anthropic"
	openaiprotocol "github.com/lucasew/mclone/pkg/protocol/openai"
)

func serveRateLimitError(w http.ResponseWriter, writer protocol.Writer, err error) bool {
	var rl *message.ErrRateLimit
	if !errors.As(err, &rl) {
		return false
	}
	setRateLimitHeaders(w, writer, rl.RetryAfter)
	w.WriteHeader(http.StatusTooManyRequests)
	switch writer.(type) {
	case *openaiprotocol.Writer:
		protocol.WriteJSON(w, map[string]any{"error": map[string]any{"message": err.Error(), "type": "rate_limit_error", "code": "rate_limit_exceeded"}})
	case *anthropicprotocol.Writer:
		protocol.WriteJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": "rate_limit_error", "message": err.Error()}})
	case responsesAPIWriter:
		protocol.WriteJSON(w, map[string]any{"type": "error", "error": map[string]any{"type": "rate_limit_error", "message": err.Error()}})
	default:
		protocol.WriteJSON(w, map[string]any{"error": err.Error()})
	}
	return true
}

func setRateLimitHeaders(w http.ResponseWriter, writer protocol.Writer, retryAfter time.Duration) {
	if retryAfter <= 0 {
		return
	}
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	switch writer.(type) {
	case *openaiprotocol.Writer, responsesAPIWriter:
		reset := (time.Duration(seconds) * time.Second).String()
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		w.Header().Set("x-ratelimit-reset-requests", reset)
		w.Header().Set("x-ratelimit-reset-tokens", reset)
	case *anthropicprotocol.Writer:
		resetAt := time.Now().Add(time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339)
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		w.Header().Set("anthropic-ratelimit-requests-reset", resetAt)
		w.Header().Set("anthropic-ratelimit-tokens-reset", resetAt)
	default:
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	}
}
