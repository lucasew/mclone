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

func (p *ToolboxProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	// Inject tool definitions, dedup by name (ours win)
	ownNames := make(map[string]bool)
	for _, t := range p.tools {
		ownNames[strings.ToLower(t.Definition.Name)] = true
	}
	var cleanTools []message.ToolDefinition
	for _, t := range options.Tools {
		if !ownNames[strings.ToLower(t.Name)] {
			cleanTools = append(cleanTools, t)
		}
	}
	for _, t := range p.tools {
		cleanTools = append(cleanTools, t.Definition)
	}
	options.Tools = cleanTools

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)
		currentMsgs := make([]message.Message, len(messages))
		copy(currentMsgs, messages)

		for loop := range p.maxLoops {
			ch, err := p.base.Chat(ctx, modelName, currentMsgs, options)
			if err != nil {
				out <- message.ChatResponse{Error: err}
				return
			}

			var assistantParts []message.Part
			var handledCalls []message.ToolCall
			var passthroughCalls []message.ToolCall

			for resp := range ch {
				if resp.Error != nil {
					out <- resp
					return
				}
				if resp.Content != "" {
					out <- resp
					assistantParts = append(assistantParts, message.TextPart{Text: resp.Content})
				}
				if resp.Thought != "" {
					out <- resp
				}
				for _, tc := range resp.ToolCalls {
					if _, ok := p.toolMap[strings.ToLower(tc.Name)]; ok {
						handledCalls = append(handledCalls, tc)
					} else {
						passthroughCalls = append(passthroughCalls, tc)
					}
				}
			}

			// No handled calls — forward passthrough + done
			if len(handledCalls) == 0 {
				if len(passthroughCalls) > 0 {
					out <- message.ChatResponse{ToolCalls: passthroughCalls}
				}
				out <- message.ChatResponse{Done: true}
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
			currentMsgs = append(currentMsgs, message.Message{
				Role: message.RoleAssistant, Parts: assistantParts,
			})

			// Execute handled calls
			for _, tc := range handledCalls {
				tool := p.toolMap[strings.ToLower(tc.Name)]
				slog.Info("toolbox_execute", "tool", tc.Name, "loop", loop)

				result, err := tool.Execute(ctx, tc.Arguments)
				if err != nil {
					result = fmt.Sprintf("Error: %v", err)
				}
				currentMsgs = append(currentMsgs, message.Message{
					Role: message.RoleTool,
					Parts: []message.Part{message.ToolResultPart{
						ToolCallID: tc.ID,
						Content:    result,
					}},
				})
			}

			// Forward passthrough calls
			if len(passthroughCalls) > 0 {
				out <- message.ChatResponse{ToolCalls: passthroughCalls}
			}

			slog.Info("toolbox_requery", "loop", loop+1, "handled", len(handledCalls))
		}

		slog.Warn("toolbox_max_loops", "max", p.maxLoops)
		out <- message.ChatResponse{Done: true}
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
			cfg.MaxLoops = 5
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
				allTools = append(allTools, t)
				toolMap[strings.ToLower(t.Definition.Name)] = t
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
