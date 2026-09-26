package toolbox

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/lucasew/mclone/pkg/tools"
)

type ToolboxConfig struct {
	Provider string   `mapstructure:"provider"`
	Tools    []string `mapstructure:"tools"`
	MaxLoops int      `mapstructure:"max_loops"`
}

type ToolboxProvider struct {
	base     remote.Provider
	tools    []tools.Tool
	toolMap  map[string]tools.Tool
	maxLoops int
}

func (p *ToolboxProvider) Name() string { return "toolbox" }

func (p *ToolboxProvider) List(ctx context.Context) ([]remote.Model, error) {
	return p.base.List(ctx)
}

func (p *ToolboxProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	// Inject tool definitions, dedup by name (ours win)
	ownNames := make(map[string]bool)
	for _, t := range p.tools {
		ownNames[strings.ToLower(t.Definition.Name)] = true
	}
	var cleanTools []message.ToolDefinition
	for _, t := range req.Options.Tools {
		if !ownNames[strings.ToLower(t.Name)] {
			cleanTools = append(cleanTools, t)
		}
	}
	for _, t := range p.tools {
		cleanTools = append(cleanTools, t.Definition)
	}
	req.Options.Tools = cleanTools

	out := make(chan message.Event)
	go func() {
		defer close(out)
		currentTurns := make([]message.Turn, len(req.Turns))
		copy(currentTurns, req.Turns)

		for loop := range p.maxLoops {
			req.Turns = currentTurns
			ch, err := p.base.Chat(ctx, req)
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
					if _, ok := p.toolMap[strings.ToLower(ev.Call.Name)]; ok {
						handledCalls = append(handledCalls, ev.Call)
					} else {
						passthroughCalls = append(passthroughCalls, ev.Call)
					}
				case message.ResponseCompleted:
					completionReason = ev.Reason
				}
			}

			// No handled calls — forward passthrough + done
			if len(handledCalls) == 0 {
				if len(passthroughCalls) > 0 {
					for _, tc := range passthroughCalls {
						out <- message.ToolCallFinished{Call: tc}
					}
				}
				out <- message.ResponseCompleted{Reason: completionReason}
				return
			}

			// Build assistant message with all tool call parts
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

			// Execute handled calls
			for _, tc := range handledCalls {
				tool := p.toolMap[strings.ToLower(tc.Name)]
				slog.Info("toolbox_execute", "tool", tc.Name, "loop", loop)
				slog.Debug("toolbox_call_args", "tool", tc.Name, "args", string(tc.Arguments))

				result, err := tool.Execute(ctx, tc.Arguments)
				if err != nil {
					result = fmt.Sprintf("Error: %v", err)
				}
				slog.Debug("toolbox_call_result", "tool", tc.Name, "result_len", len(result), "result", result)
				currentTurns = append(currentTurns, message.Turn{
					Role: message.RoleTool,
					Parts: []message.Part{message.ToolResultPart{
						ToolCallID: tc.ID,
						Content:    result,
					}},
				})
			}

			// Forward passthrough calls
			if len(passthroughCalls) > 0 {
				for _, tc := range passthroughCalls {
					out <- message.ToolCallFinished{Call: tc}
				}
			}

			slog.Info("toolbox_requery", "loop", loop+1, "handled", len(handledCalls))
		}

		// Exhausting the loop budget means the model kept requesting owned
		// tools without producing a terminal reply. Completing with end_turn
		// looks like success to clients; surface an error instead.
		err := fmt.Errorf("toolbox: max tool loops (%d) exceeded", p.maxLoops)
		slog.Warn("toolbox_max_loops", "max", p.maxLoops)
		out <- message.ResponseError{Err: err}
	}()
	return out, nil
}

func init() {
	remote.Register("toolbox", func(name string, options map[string]any, resolve remote.Resolver) (remote.Provider, error) {
		var cfg ToolboxConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		if cfg.Provider == "" {
			return nil, fmt.Errorf("toolbox requires 'provider' option")
		}
		if cfg.MaxLoops == 0 {
			cfg.MaxLoops = 20
		}

		base, err := resolve.Provider(cfg.Provider)
		if err != nil {
			return nil, fmt.Errorf("toolbox: failed to resolve provider %q: %w", cfg.Provider, err)
		}

		var allTools []tools.Tool
		toolMap := make(map[string]tools.Tool)

		for _, tn := range cfg.Tools {
			tn = strings.TrimSpace(tn)
			if tn == "" {
				continue
			}

			source, err := resolve.ToolSource(tn)
			if err != nil {
				return nil, fmt.Errorf("toolbox: failed to resolve tool source %q: %w", tn, err)
			}
			ts, err := source.Tools(context.Background())
			if err != nil {
				return nil, fmt.Errorf("toolbox: failed to get tools from %q: %w", tn, err)
			}
			for _, t := range ts {
				key := strings.ToLower(t.Definition.Name)
				if existing, ok := toolMap[key]; ok {
					slog.Warn("toolbox_tool_collision",
						"tool", t.Definition.Name,
						"source", tn,
						"overrides", existing.Definition.Name,
					)
				}
				allTools = append(allTools, t)
				toolMap[key] = t
			}
		}

		return &ToolboxProvider{
			base:     base,
			tools:    allTools,
			toolMap:  toolMap,
			maxLoops: cfg.MaxLoops,
		}, nil
	})
}
