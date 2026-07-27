package remote

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/tools"
	"github.com/mitchellh/mapstructure"
)

type Factory func(name string, options map[string]any, resolve Resolver) (Provider, error)

// Resolver provides access to provider and tool source resolution.
type Resolver struct {
	Provider      func(remoteName string) (Provider, error)
	Exported      func() (Provider, error)
	ToolSource    func(toolName string) (tools.ToolSource, error)
	UpdateOptions func(remoteName string, options map[string]any) error
}

var registry = make(map[string]Factory)

func Register(typeName string, factory Factory) {
	registry[typeName] = factory
}

func ListTypes() []string {
	var types []string
	for t := range registry {
		types = append(types, t)
	}
	return types
}

// NewResolver creates a Resolver from a ConfigLoader.
// It supports implicit balance groups via the ":" naming convention:
// remotes named "anth:1", "anth:2" form an implicit balance group "anth".
func NewResolver(loader *config.ConfigLoader) Resolver {
	conf, err := loader.Load()
	if err != nil {
		panic(fmt.Sprintf("failed to load config: %v", err))
	}

	providerCache := make(map[string]Provider)
	toolSourceCache := make(map[string]tools.ToolSource)

	var resolve Resolver

	resolve.Provider = func(remoteName string) (Provider, error) {
		if p, ok := providerCache[remoteName]; ok {
			return p, nil
		}

		p, err := resolveOne(conf, remoteName, resolve)
		if err != nil {
			return nil, err
		}
		if rc, ok := conf.Remotes[remoteName]; ok {
			p = wrapProvider(rc, p)
		}
		providerCache[remoteName] = p
		return p, nil
	}

	resolve.Exported = func() (Provider, error) {
		var remoteNames []string
		for name, rc := range conf.Remotes {
			if rc.Export {
				remoteNames = append(remoteNames, name)
			}
		}
		slices.Sort(remoteNames)

		if len(remoteNames) == 0 {
			return nil, fmt.Errorf("no exported remotes configured")
		}

		merged := &exportedProvider{models: make(map[string]exportedModel)}
		for _, name := range remoteNames {
			prov, err := resolve.Provider(name)
			if err != nil {
				return nil, fmt.Errorf("exported remote %q: %w", name, err)
			}
			merged.backends = append(merged.backends, exportedBackend{name: name, provider: prov})
		}

		for name, tc := range conf.Tools {
			if !tc.Export {
				continue
			}
			source, err := resolve.ToolSource(name)
			if err != nil {
				return nil, fmt.Errorf("exported tool %q: %w", name, err)
			}
			exportedTools, err := source.Tools(context.Background())
			if err != nil {
				return nil, fmt.Errorf("exported tool %q: %w", name, err)
			}
			if merged.toolMap == nil {
				merged.toolMap = make(map[string]tools.Tool)
			}
			for _, tool := range exportedTools {
				key := strings.ToLower(tool.Definition.Name)
				if existing, ok := merged.toolMap[key]; ok {
					slog.Warn("exported_tool_collision",
						"tool", tool.Definition.Name,
						"source", name,
						"overrides", existing.Definition.Name,
					)
				}
				merged.tools = append(merged.tools, tool)
				merged.toolMap[key] = tool
			}
		}
		return merged, nil
	}

	resolve.ToolSource = func(toolName string) (tools.ToolSource, error) {
		if ts, ok := toolSourceCache[toolName]; ok {
			return ts, nil
		}

		tc, ok := conf.Tools[toolName]
		if !ok {
			return nil, fmt.Errorf("tool %q not found in config", toolName)
		}
		ts, err := tools.New(tc.Type, toolName, tc.Options)
		if err != nil {
			return nil, err
		}
		toolSourceCache[toolName] = ts
		return ts, nil
	}

	resolve.UpdateOptions = func(remoteName string, options map[string]any) error {
		// Reload config to avoid overwriting concurrent changes
		c, err := loader.Load()
		if err != nil {
			return err
		}
		rc, ok := c.Remotes[remoteName]
		if !ok {
			return fmt.Errorf("remote %q not found during update", remoteName)
		}

		slog.Info("updating_options", "remote", remoteName, "keys", len(options))

		if rc.Options == nil {
			rc.Options = make(map[string]any)
		}
		for k, v := range options {
			rc.Options[k] = v
		}
		c.Remotes[remoteName] = rc

		return loader.Save(c)
	}

	return resolve
}

func resolveOne(conf *config.Config, remoteName string, resolve Resolver) (Provider, error) {
	rc, exactMatch := conf.Remotes[remoteName]

	// Exact match with a type → normal remote
	if exactMatch && rc.Type != "" {
		factory, ok := registry[rc.Type]
		if !ok {
			return nil, fmt.Errorf("unknown provider type: %s", rc.Type)
		}
		return factory(remoteName, rc.Options, resolve)
	}

	// Try implicit balance: find all "remoteName:*" entries
	prefix := remoteName + ":"
	var members []string
	for name, rc := range conf.Remotes {
		if strings.HasPrefix(name, prefix) && rc.Type != "" {
			members = append(members, name)
		}
	}
	slices.Sort(members)

	if len(members) == 0 {
		if exactMatch {
			return nil, fmt.Errorf("remote %q has no type and no %s:* members", remoteName, remoteName)
		}
		return nil, fmt.Errorf("remote %q not found", remoteName)
	}

	if len(members) == 1 {
		return resolve.Provider(members[0])
	}

	// Build implicit balance group
	groupOpts := map[string]any{}
	if exactMatch {
		groupOpts = rc.Options
	}

	factory, ok := registry["balance"]
	if !ok {
		return nil, fmt.Errorf("balance provider not registered")
	}

	opts := make(map[string]any)
	for k, v := range groupOpts {
		opts[k] = v
	}
	opts["remotes"] = strings.Join(members, ",")

	for _, m := range members {
		if mc, ok := conf.Remotes[m]; ok && mc.Options != nil {
			if ft, ok := mc.Options["failover_threshold"]; ok {
				opts["failover_threshold:"+m] = ft
			}
		}
	}

	return factory(remoteName, opts, resolve)
}

func NewProvider(typeName string, name string, options map[string]any) (Provider, error) {
	factory, ok := registry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown provider type: %s", typeName)
	}
	return factory(name, options, Resolver{})
}

func wrapProvider(rc config.RemoteConfig, p Provider) Provider {
	if rc.MaxConcurrent > 0 {
		return newConcurrencyProvider(p, rc.MaxConcurrent)
	}
	return p
}

type concurrencyProvider struct {
	inner Provider
	sem   chan struct{}
}

func newConcurrencyProvider(inner Provider, maxConcurrent int) Provider {
	return &concurrencyProvider{
		inner: inner,
		sem:   make(chan struct{}, maxConcurrent),
	}
}

func (p *concurrencyProvider) Name() string { return p.inner.Name() }

func (p *concurrencyProvider) List(ctx context.Context) ([]Model, error) {
	return p.inner.List(ctx)
}

func (p *concurrencyProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	ch, err := p.inner.Chat(ctx, req)
	if err != nil {
		<-p.sem
		return nil, err
	}

	out := make(chan message.Event)
	go func() {
		defer close(out)
		defer func() { <-p.sem }()
		for ev := range ch {
			out <- ev
		}
	}()
	return out, nil
}

type exportedBackend struct {
	name     string
	provider Provider
}

type exportedModel struct {
	backend exportedBackend
	model   Model
}

type exportedProvider struct {
	backends []exportedBackend
	models   map[string]exportedModel
	tools    []tools.Tool
	toolMap  map[string]tools.Tool
}

func (p *exportedProvider) Name() string { return "exported" }

func (p *exportedProvider) List(ctx context.Context) ([]Model, error) {
	if len(p.models) > 0 {
		return p.sortedModels(), nil
	}

	var conflicts []string
	for _, backend := range p.backends {
		models, err := backend.provider.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list models from %q: %w", backend.name, err)
		}
		for _, model := range models {
			if existing, ok := p.models[model.Slug]; ok {
				conflicts = append(conflicts, fmt.Sprintf("%s (%s, %s)", model.Slug, existing.backend.name, backend.name))
				continue
			}
			model.OwnedBy = []string{backend.name}
			p.models[model.Slug] = exportedModel{backend: backend, model: model}
		}
	}

	if len(conflicts) > 0 {
		slices.Sort(conflicts)
		return nil, fmt.Errorf("exported model slug conflicts: %s", strings.Join(conflicts, ", "))
	}

	return p.sortedModels(), nil
}

func (p *exportedProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	if len(p.tools) > 0 {
		return p.chatWithTools(ctx, req)
	}
	return p.chatBase(ctx, req)
}

func (p *exportedProvider) chatBase(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	if len(p.models) == 0 {
		if _, err := p.List(ctx); err != nil {
			return nil, err
		}
	}

	entry, ok := p.models[req.Model]
	if !ok {
		return nil, fmt.Errorf("model %q not exported", req.Model)
	}
	return entry.backend.provider.Chat(ctx, req)
}

func (p *exportedProvider) chatWithTools(ctx context.Context, req message.Request) (<-chan message.Event, error) {
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

		for loop := 0; loop < 20; loop++ {
			req.Turns = currentTurns
			ch, err := p.chatBase(ctx, req)
			if err != nil {
				out <- message.ResponseError{Err: err}
				return
			}

			var assistantParts []message.Part
			var handledCalls []message.ToolCall
			var passthroughCalls []message.ToolCall

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
				}
			}

			if len(handledCalls) == 0 {
				for _, tc := range passthroughCalls {
					out <- message.ToolCallFinished{Call: tc}
				}
				out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
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
				tool := p.toolMap[strings.ToLower(tc.Name)]
				result, err := tool.Execute(ctx, tc.Arguments)
				if err != nil {
					result = fmt.Sprintf("Error: %v", err)
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

			slog.Info("exported_toolbox_requery", "loop", loop+1, "handled", len(handledCalls))
		}

		slog.Warn("exported_toolbox_max_loops", "max", 20)
		out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
	}()
	return out, nil
}

func (p *exportedProvider) sortedModels() []Model {
	models := make([]Model, 0, len(p.models))
	for _, entry := range p.models {
		models = append(models, entry.model)
	}
	slices.SortFunc(models, func(a, b Model) int {
		return cmp.Compare(a.Slug, b.Slug)
	})
	return models
}

// DecodeOptions decodes input into output using mapstructure with weak typing.
func DecodeOptions(input any, output any) error {
	config := &mapstructure.DecoderConfig{
		Metadata:         nil,
		Result:           output,
		WeaklyTypedInput: true,
		TagName:          "mapstructure",
	}

	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return fmt.Errorf("mapstructure decoder: %w", err)
	}
	if err := decoder.Decode(input); err != nil {
		return fmt.Errorf("decode options: %w", err)
	}
	return nil
}
