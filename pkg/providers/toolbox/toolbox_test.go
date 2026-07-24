package toolbox

import (
	"context"
	"strings"
	"testing"

	json "github.com/goccy/go-json"

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

// loopingOwnedToolProvider always requests a tool owned by the toolbox so
// Chat keeps looping until maxLoops is exhausted.
type loopingOwnedToolProvider struct{}

func (p *loopingOwnedToolProvider) Name() string { return "looping" }

func (p *loopingOwnedToolProvider) List(context.Context) ([]remote.Model, error) {
	return nil, nil
}

func (p *loopingOwnedToolProvider) Chat(context.Context, message.Request) (<-chan message.Event, error) {
	ch := make(chan message.Event, 2)
	ch <- message.ToolCallFinished{
		Call: message.ToolCall{
			ID:        "call_owned",
			Name:      "echo",
			Arguments: []byte(`{"x":"1"}`),
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

func TestToolboxMaxLoopsReturnsResponseError(t *testing.T) {
	echo := tools.Tool{
		Definition: message.ToolDefinition{
			Type: "function",
			Name: "echo",
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			return string(args), nil
		},
	}
	provider := &ToolboxProvider{
		base: &loopingOwnedToolProvider{},
		tools: []tools.Tool{echo},
		toolMap: map[string]tools.Tool{
			"echo": echo,
		},
		maxLoops: 2,
	}

	ch, err := provider.Chat(context.Background(), message.Request{Model: "demo"})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	var sawCompleted bool
	var finalErr error
	for event := range ch {
		switch ev := event.(type) {
		case message.ResponseCompleted:
			sawCompleted = true
		case message.ResponseError:
			finalErr = ev.Err
		}
	}

	if sawCompleted {
		t.Fatal("expected no ResponseCompleted when max loops is exceeded")
	}
	if finalErr == nil {
		t.Fatal("expected ResponseError when max loops is exceeded")
	}
	if !strings.Contains(finalErr.Error(), "max tool loops") {
		t.Fatalf("error = %q, want message mentioning max tool loops", finalErr)
	}
}
