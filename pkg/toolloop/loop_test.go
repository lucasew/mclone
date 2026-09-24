package toolloop

import (
	"context"
	"errors"
	"testing"

	json "github.com/goccy/go-json"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/tools"
)

var ErrTestLoopBudget = errors.New("max loops")

var ErrTestToolBoom = errors.New("boom")

func TestMergeDefinitionsOwnedWinsCaseInsensitive(t *testing.T) {
	t.Parallel()
	owned := []tools.Tool{{
		Definition: message.ToolDefinition{Name: "Echo", Description: "owned"},
	}}
	requested := []message.ToolDefinition{
		{Name: "echo", Description: "request"},
		{Name: "other", Description: "keep"},
	}
	got := MergeDefinitions(requested, owned)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Name != "other" || got[0].Description != "keep" {
		t.Fatalf("kept request = %#v", got[0])
	}
	if got[1].Name != "Echo" || got[1].Description != "owned" {
		t.Fatalf("owned = %#v", got[1])
	}
}

func TestRunPreservesStopReasonForPassthrough(t *testing.T) {
	t.Parallel()
	ch, err := Run(t.Context(), message.Request{}, Config{
		MaxLoops: 1,
		ToolMap:  map[string]tools.Tool{},
		Chat: func(context.Context, message.Request) (<-chan message.Event, error) {
			out := make(chan message.Event, 2)
			out <- message.ToolCallFinished{Call: message.ToolCall{ID: "c", Name: "exec"}}
			out <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
			close(out)
			return out, nil
		},
		PreserveStopReason: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var reason message.StopReason
	var sawCall bool
	for ev := range ch {
		switch v := ev.(type) {
		case message.ToolCallFinished:
			sawCall = true
		case message.ResponseCompleted:
			reason = v.Reason
		case message.ResponseError:
			t.Fatalf("error: %v", v.Err)
		}
	}
	if !sawCall || reason != message.StopReasonToolCall {
		t.Fatalf("sawCall=%v reason=%q", sawCall, reason)
	}
}

func TestRunEndTurnWhenStopReasonNotPreserved(t *testing.T) {
	t.Parallel()
	ch, err := Run(t.Context(), message.Request{}, Config{
		MaxLoops: 1,
		ToolMap:  map[string]tools.Tool{},
		Chat: func(context.Context, message.Request) (<-chan message.Event, error) {
			out := make(chan message.Event, 1)
			out <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
			close(out)
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reason message.StopReason
	for ev := range ch {
		if v, ok := ev.(message.ResponseCompleted); ok {
			reason = v.Reason
		}
	}
	if reason != message.StopReasonEndTurn {
		t.Fatalf("reason = %q, want end_turn", reason)
	}
}

func TestRunFeedsOwnedToolResultBack(t *testing.T) {
	t.Parallel()
	echo := tools.Tool{
		Definition: message.ToolDefinition{Name: "echo"},
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "pong", nil
		},
	}
	var calls int
	ch, err := Run(t.Context(), message.Request{}, Config{
		MaxLoops: 3,
		ToolMap:  map[string]tools.Tool{"echo": echo},
		Chat: func(_ context.Context, req message.Request) (<-chan message.Event, error) {
			calls++
			out := make(chan message.Event, 2)
			if calls == 1 {
				out <- message.ToolCallFinished{Call: message.ToolCall{
					ID: "1", Name: "echo", Arguments: json.RawMessage(`{}`),
				}}
				out <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
			} else {
				var sawResult bool
				for _, turn := range req.Turns {
					for _, part := range turn.Parts {
						if res, ok := part.(message.ToolResultPart); ok && res.Content == "pong" && res.ToolCallID == "1" {
							sawResult = true
						}
					}
				}
				if !sawResult {
					t.Errorf("second chat missing tool result: %#v", req.Turns)
				}
				out <- message.TextDelta{Text: "done"}
				out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
			}
			close(out)
			return out, nil
		},
		PreserveStopReason: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var reason message.StopReason
	for ev := range ch {
		switch v := ev.(type) {
		case message.TextDelta:
			text += v.Text
		case message.ResponseCompleted:
			reason = v.Reason
		case message.ToolCallFinished:
			t.Fatalf("owned call forwarded: %#v", v.Call)
		case message.ResponseError:
			t.Fatalf("error: %v", v.Err)
		}
	}
	if text != "done" || reason != message.StopReasonEndTurn || calls != 2 {
		t.Fatalf("text=%q reason=%q calls=%d", text, reason, calls)
	}
}

func TestRunExhaustedError(t *testing.T) {
	t.Parallel()
	want := ErrTestLoopBudget
	echo := tools.Tool{
		Definition: message.ToolDefinition{Name: "echo"},
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "x", nil
		},
	}
	ch, err := Run(t.Context(), message.Request{}, Config{
		MaxLoops: 2,
		ToolMap:  map[string]tools.Tool{"echo": echo},
		Chat: func(context.Context, message.Request) (<-chan message.Event, error) {
			out := make(chan message.Event, 1)
			out <- message.ToolCallFinished{Call: message.ToolCall{ID: "1", Name: "echo"}}
			close(out)
			return out, nil
		},
		Exhausted: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got error
	for ev := range ch {
		switch v := ev.(type) {
		case message.ResponseCompleted:
			t.Fatal("unexpected completion")
		case message.ResponseError:
			got = v.Err
		}
	}
	if !errors.Is(got, want) {
		t.Fatalf("err = %v, want %v", got, want)
	}
}

func TestRunExhaustedCompletesWhenNoError(t *testing.T) {
	t.Parallel()
	echo := tools.Tool{
		Definition: message.ToolDefinition{Name: "echo"},
		Execute:    func(context.Context, json.RawMessage) (string, error) { return "x", nil },
	}
	ch, err := Run(t.Context(), message.Request{}, Config{
		MaxLoops: 1,
		ToolMap:  map[string]tools.Tool{"echo": echo},
		Chat: func(context.Context, message.Request) (<-chan message.Event, error) {
			out := make(chan message.Event, 1)
			out <- message.ToolCallFinished{Call: message.ToolCall{ID: "1", Name: "echo"}}
			close(out)
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reason message.StopReason
	var sawErr bool
	for ev := range ch {
		switch v := ev.(type) {
		case message.ResponseCompleted:
			reason = v.Reason
		case message.ResponseError:
			sawErr = true
		}
	}
	if sawErr || reason != message.StopReasonEndTurn {
		t.Fatalf("sawErr=%v reason=%q", sawErr, reason)
	}
}

func TestRunExecuteErrorBecomesToolContent(t *testing.T) {
	t.Parallel()
	boom := ErrTestToolBoom
	echo := tools.Tool{
		Definition: message.ToolDefinition{Name: "echo"},
		Execute: func(context.Context, json.RawMessage) (string, error) {
			return "", boom
		},
	}
	var calls int
	var saw string
	ch, err := Run(t.Context(), message.Request{}, Config{
		MaxLoops: 2,
		ToolMap:  map[string]tools.Tool{"echo": echo},
		Chat: func(_ context.Context, req message.Request) (<-chan message.Event, error) {
			calls++
			out := make(chan message.Event, 2)
			if calls == 1 {
				out <- message.ToolCallFinished{Call: message.ToolCall{ID: "1", Name: "echo"}}
			} else {
				for _, turn := range req.Turns {
					for _, part := range turn.Parts {
						if res, ok := part.(message.ToolResultPart); ok {
							saw = res.Content
						}
					}
				}
				out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
			}
			close(out)
			return out, nil
		},
		PreserveStopReason: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if saw != "Error: boom" {
		t.Fatalf("tool content = %q", saw)
	}
}
