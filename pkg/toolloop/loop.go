// Package toolloop runs owned tools inside a provider chat and re-queries the
// base model until a turn has no owned calls or the loop budget is spent.
package toolloop

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/tools"
)

// MergeDefinitions drops requested tools whose names collide with owned tools
// (case-insensitive) and appends the owned definitions. Owned tools win.
func MergeDefinitions(requested []message.ToolDefinition, owned []tools.Tool) []message.ToolDefinition {
	ownNames := make(map[string]bool, len(owned))
	for _, t := range owned {
		ownNames[strings.ToLower(t.Definition.Name)] = true
	}
	var clean []message.ToolDefinition
	for _, t := range requested {
		if !ownNames[strings.ToLower(t.Name)] {
			clean = append(clean, t)
		}
	}
	for _, t := range owned {
		clean = append(clean, t.Definition)
	}
	return clean
}

// Config controls one owned-tool chat loop.
type Config struct {
	MaxLoops int
	ToolMap  map[string]tools.Tool
	Chat     func(ctx context.Context, req message.Request) (<-chan message.Event, error)

	// PreserveStopReason keeps the base chat's ResponseCompleted reason when
	// the turn has no owned calls. Otherwise that turn completes with end_turn.
	PreserveStopReason bool

	// Exhausted, when non-nil, is sent as ResponseError after MaxLoops owned
	// rounds. Nil sends ResponseCompleted with end_turn.
	Exhausted error

	RequeryMsg   string
	ExhaustedMsg string

	BeforeExecute func(loop int, call message.ToolCall)
	AfterExecute  func(loop int, call message.ToolCall, result string)
}

// Run drives owned-tool execution. The returned channel is closed when the
// loop finishes. Chat errors and stream errors are forwarded as ResponseError.
func Run(ctx context.Context, req message.Request, cfg Config) (<-chan message.Event, error) {
	out := make(chan message.Event)
	go func() {
		defer close(out)
		currentTurns := make([]message.Turn, len(req.Turns))
		copy(currentTurns, req.Turns)

		for loop := 0; loop < cfg.MaxLoops; loop++ {
			req.Turns = currentTurns
			ch, err := cfg.Chat(ctx, req)
			if err != nil {
				out <- message.ResponseError{Err: err}
				return
			}

			var assistantParts []message.Part
			var handledCalls []message.ToolCall
			var passthroughCalls []message.ToolCall
			completionReason := message.StopReasonEndTurn

			for event := range ch {
				switch ev := event.(type) {
				case message.ResponseError:
					out <- ev
					return
				case message.TextDelta:
					out <- ev
					assistantParts = append(assistantParts, message.TextPart{Text: ev.Text})
				case message.ReasoningDelta:
					out <- ev
				case message.ToolCallFinished:
					if _, ok := cfg.ToolMap[strings.ToLower(ev.Call.Name)]; ok {
						handledCalls = append(handledCalls, ev.Call)
					} else {
						passthroughCalls = append(passthroughCalls, ev.Call)
					}
				case message.ResponseCompleted:
					if cfg.PreserveStopReason {
						completionReason = ev.Reason
					}
				}
			}

			if len(handledCalls) == 0 {
				for _, tc := range passthroughCalls {
					out <- message.ToolCallFinished{Call: tc}
				}
				reason := message.StopReasonEndTurn
				if cfg.PreserveStopReason {
					reason = completionReason
				}
				out <- message.ResponseCompleted{Reason: reason}
				return
			}

			for _, tc := range handledCalls {
				assistantParts = append(assistantParts, message.ToolCallPart{
					ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
			for _, tc := range passthroughCalls {
				assistantParts = append(assistantParts, message.ToolCallPart{
					ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
			currentTurns = append(currentTurns, message.Turn{
				Role: message.RoleAssistant, Parts: assistantParts,
			})

			for _, tc := range handledCalls {
				if cfg.BeforeExecute != nil {
					cfg.BeforeExecute(loop, tc)
				}
				tool := cfg.ToolMap[strings.ToLower(tc.Name)]
				result, err := tool.Execute(ctx, tc.Arguments)
				if err != nil {
					result = fmt.Sprintf("Error: %v", err)
				}
				if cfg.AfterExecute != nil {
					cfg.AfterExecute(loop, tc, result)
				}
				currentTurns = append(currentTurns, message.Turn{
					Role: message.RoleTool,
					Parts: []message.Part{message.ToolResultPart{
						ToolCallID: tc.ID,
						Content:    result,
					}},
				})
			}

			for _, tc := range passthroughCalls {
				out <- message.ToolCallFinished{Call: tc}
			}

			if cfg.RequeryMsg != "" {
				slog.Info(cfg.RequeryMsg, "loop", loop+1, "handled", len(handledCalls))
			}
		}

		if cfg.ExhaustedMsg != "" {
			slog.Warn(cfg.ExhaustedMsg, "max", cfg.MaxLoops)
		}
		if cfg.Exhausted != nil {
			out <- message.ResponseError{Err: cfg.Exhausted}
			return
		}
		out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
	}()
	return out, nil
}
