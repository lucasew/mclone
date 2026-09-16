package anthropic

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lucasew/mclone/pkg/message"
)

func TestServeStreamSurfacesResponseError(t *testing.T) {
	ch := make(chan message.Event, 1)
	ch <- message.ResponseError{Err: errors.New("secret backend token=xyz")}
	close(ch)

	rr := httptest.NewRecorder()
	NewWriter().ServeResponse(rr, ch, "m", true)
	body := rr.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("missing error event: %q", body)
	}
	if !strings.Contains(body, "internal server error") {
		t.Fatalf("missing generic message: %q", body)
	}
	if strings.Contains(body, "token=xyz") || strings.Contains(body, "secret backend") {
		t.Fatalf("leaked internal error: %q", body)
	}
	// Must not claim successful end_turn after error
	if strings.Contains(body, "end_turn") || strings.Contains(body, "message_stop") {
		t.Fatalf("stream continued after error: %q", body)
	}
}

func TestServeJSONSurfacesResponseError(t *testing.T) {
	ch := make(chan message.Event, 1)
	ch <- message.ResponseError{Err: errors.New("secret backend token=xyz")}
	close(ch)

	rr := httptest.NewRecorder()
	NewWriter().ServeResponse(rr, ch, "m", false)
	if rr.Code != 500 {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "internal server error") {
		t.Fatalf("body=%q", body)
	}
	if strings.Contains(body, "token=xyz") {
		t.Fatalf("leaked: %q", body)
	}
}
