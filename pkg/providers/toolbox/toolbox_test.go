package toolbox

import (
	"context"
	"testing"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/lucasew/mclone/pkg/tools"
)

type passthroughProvider struct{}

func (p *passthroughProvider) Name() string { return "passthrough" }

func (p *passthroughProvider) List(context.Context) ([]remote.Model, error) { return nil, nil }

func (p *passthroughProvider) Chat(context.Context, message.Request) (<-chan message.Event, error) {
	ch := make(chan message.Event, 2)
	ch <- message.ToolCallFinished{
		Call: message.ToolCall{
			ID:        "call_1",
			Name:      "exec",
			Arguments: []byte(`{"command":"ls"}`),
		},
	}
	ch <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
	close(ch)
	return ch, nil
}

func TestToolboxPreservesToolCallFinishReasonForPassthroughCalls(t *testing.T) {
	provider := &ToolboxProvider{
		base:     &passthroughProvider{},
		toolMap:  map[string]tools.Tool{},
		maxLoops: 1,
	}

	ch, err := provider.Chat(context.Background(), message.Request{Model: "demo"})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	var sawToolCall bool
	var finalReason message.StopReason
	for event := range ch {
		switch ev := event.(type) {
		case message.ToolCallFinished:
			sawToolCall = true
		case message.ResponseCompleted:
			finalReason = ev.Reason
		case message.ResponseError:
			t.Fatalf("unexpected response error: %v", ev.Err)
		}
	}

	if !sawToolCall {
		t.Fatal("expected passthrough tool call")
	}
	if finalReason != message.StopReasonToolCall {
		t.Fatalf("expected finish reason %q, got %q", message.StopReasonToolCall, finalReason)
	}
}
