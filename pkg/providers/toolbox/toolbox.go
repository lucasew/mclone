package toolbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/lucasew/mclone/pkg/toolloop"
	"github.com/lucasew/mclone/pkg/tools"
)

var (
	ErrMaxLoops         = errors.New("toolbox: max tool loops")
	ErrProviderRequired = errors.New("toolbox requires 'provider' option")
)

type ToolboxConfig struct {
	Provider string   `mapstructure:"provider"`
	Tools    []string `mapstructure:"tools"`
	MaxLoops int      `mapstructure:"max_loops"`
}

type ToolboxProvider struct {
	base      remote.Provider
	resolve   remote.Resolver
	toolNames []string
	maxLoops  int

	toolsMu sync.Mutex
	tools   []tools.Tool
	toolMap map[string]tools.Tool
	loaded  bool
}

func (p *ToolboxProvider) Name() string { return "toolbox" }

func (p *ToolboxProvider) List(ctx context.Context) ([]remote.Model, error) {
	return p.base.List(ctx)
}

func (p *ToolboxProvider) ensureTools(ctx context.Context) error {
	p.toolsMu.Lock()
	defer p.toolsMu.Unlock()
	if p.loaded {
		return nil
	}

	allTools := make([]tools.Tool, 0)
	toolMap := make(map[string]tools.Tool)
	for _, tn := range p.toolNames {
		tn = strings.TrimSpace(tn)
		if tn == "" {
			continue
		}
		source, err := p.resolve.ToolSource(tn)
		if err != nil {
			return fmt.Errorf("toolbox: failed to resolve tool source %q: %w", tn, err)
		}
		ts, err := source.Tools(ctx)
		if err != nil {
			return fmt.Errorf("toolbox: failed to get tools from %q: %w", tn, err)
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
	p.tools = allTools
	p.toolMap = toolMap
	p.loaded = true
	return nil
}

func (p *ToolboxProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	if err := p.ensureTools(ctx); err != nil {
		return nil, err
	}

	req.Options.Tools = toolloop.MergeDefinitions(req.Options.Tools, p.tools)

	// Exhausting the loop budget means the model kept requesting owned
	// tools without producing a terminal reply. Completing with end_turn
	// looks like success to clients; surface an error instead.
	return toolloop.Run(ctx, req, toolloop.Config{
		MaxLoops:           p.maxLoops,
		ToolMap:            p.toolMap,
		Chat:               p.base.Chat,
		PreserveStopReason: true,
		Exhausted:          fmt.Errorf("%w: %d exceeded", ErrMaxLoops, p.maxLoops),
		RequeryMsg:         "toolbox_requery",
		ExhaustedMsg:       "toolbox_max_loops",
		BeforeExecute: func(loop int, call message.ToolCall) {
			slog.Info("toolbox_execute", "tool", call.Name, "loop", loop)
			slog.Debug("toolbox_call_args", "tool", call.Name, "args", string(call.Arguments))
		},
		AfterExecute: func(loop int, call message.ToolCall, result string) {
			slog.Debug("toolbox_call_result", "tool", call.Name, "result_len", len(result), "result", result)
		},
	})
}

func init() {
	remote.Register("toolbox", func(name string, options map[string]any, resolve remote.Resolver) (remote.Provider, error) {
		var cfg ToolboxConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		if cfg.Provider == "" {
			return nil, ErrProviderRequired
		}
		if cfg.MaxLoops == 0 {
			cfg.MaxLoops = 20
		}

		base, err := resolve.Provider(cfg.Provider)
		if err != nil {
			return nil, fmt.Errorf("toolbox: failed to resolve provider %q: %w", cfg.Provider, err)
		}

		return &ToolboxProvider{
			base:      base,
			resolve:   resolve,
			toolNames: cfg.Tools,
			maxLoops:  cfg.MaxLoops,
		}, nil
	})
}
