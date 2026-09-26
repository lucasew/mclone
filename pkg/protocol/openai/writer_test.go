package openai

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lucasew/mclone/pkg/message"
)

func TestServeJSONUsesToolCallsFinishReason(t *testing.T) {
	ch := make(chan message.Event, 2)
	ch <- message.ToolCallFinished{
		Call: message.ToolCall{
			ID:        "call_1",
			Name:      "exec",
			Arguments: []byte(`{"command":"echo hi"}`),
		},
	}
	ch <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
	close(ch)

	rr := httptest.NewRecorder()
	NewWriter().serveJSON(rr, ch, "claw")

	body := rr.Body.String()
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected finish_reason tool_calls, got %s", body)
	}
	if !strings.Contains(body, `"tool_calls"`) {
		t.Fatalf("expected tool_calls in body, got %s", body)
	}
}
