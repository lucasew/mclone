package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/tools"
	"github.com/pelletier/go-toml/v2"
)

type testProvider struct {
	models []Model
}

func (p *testProvider) Name() string { return "test" }

func (p *testProvider) List(context.Context) ([]Model, error) { return p.models, nil }

func (p *testProvider) Chat(context.Context, message.Request) (<-chan message.Event, error) {
	ch := make(chan message.Event)
	close(ch)
	return ch, nil
}

func TestExportedProviderListsExportedRemotes(t *testing.T) {
	orig := registry
	registry = map[string]Factory{}
	t.Cleanup(func() { registry = orig })

	Register("test_exported_ok", func(name string, options map[string]any, resolve Resolver) (Provider, error) {
		slug, _ := options["slug"].(string)
		return &testProvider{models: []Model{{Slug: slug, Name: slug}}}, nil
	})

	conf := &config.Config{
		Remotes: map[string]config.RemoteConfig{
			"a": {Type: "test_exported_ok", Export: true, Options: map[string]any{"slug": "alpha"}},
			"b": {Type: "test_exported_ok", Export: true, Options: map[string]any{"slug": "beta"}},
			"c": {Type: "test_exported_ok", Export: false, Options: map[string]any{"slug": "gamma"}},
		},
	}

	resolve := NewResolver(writeTestConfig(t, conf))
	provider, err := resolve.Exported()
	if err != nil {
		t.Fatalf("Exported() error = %v", err)
	}

	models, err := provider.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Slug != "alpha" || models[1].Slug != "beta" {
		t.Fatalf("unexpected models: %#v", models)
	}
	if len(models[0].OwnedBy) != 1 || models[0].OwnedBy[0] != "a" {
		t.Fatalf("unexpected alpha owner: %#v", models[0].OwnedBy)
	}
	if len(models[1].OwnedBy) != 1 || models[1].OwnedBy[0] != "b" {
		t.Fatalf("unexpected beta owner: %#v", models[1].OwnedBy)
	}
}

func TestExportedProviderDetectsConflicts(t *testing.T) {
	orig := registry
	registry = map[string]Factory{}
	t.Cleanup(func() { registry = orig })

	Register("test_exported_conflict", func(name string, options map[string]any, resolve Resolver) (Provider, error) {
		return &testProvider{models: []Model{{Slug: "same", Name: "same"}}}, nil
	})

	conf := &config.Config{
		Remotes: map[string]config.RemoteConfig{
			"a": {Type: "test_exported_conflict", Export: true},
			"b": {Type: "test_exported_conflict", Export: true},
		},
	}

	resolve := NewResolver(writeTestConfig(t, conf))
	provider, err := resolve.Exported()
	if err != nil {
		t.Fatalf("Exported() error = %v", err)
	}

	if _, err := provider.List(context.Background()); err == nil {
		t.Fatal("expected conflict error, got nil")
	}
}

func TestExportedProviderIncludesImplicitBalanceGroup(t *testing.T) {
	orig := registry
	registry = map[string]Factory{}
	t.Cleanup(func() { registry = orig })

	Register("test_exported_group", func(name string, options map[string]any, resolve Resolver) (Provider, error) {
		slug, _ := options["slug"].(string)
		return &testProvider{models: []Model{{Slug: slug, Name: name}}}, nil
	})

	Register("balance", func(name string, options map[string]any, resolve Resolver) (Provider, error) {
		remotesCSV, _ := options["remotes"].(string)
		remoteNames := strings.Split(remotesCSV, ",")
		var models []Model
		for _, remoteName := range remoteNames {
			prov, err := resolve.Provider(remoteName)
			if err != nil {
				return nil, err
			}
			listed, err := prov.List(context.Background())
			if err != nil {
				return nil, err
			}
			models = append(models, listed...)
		}
		return &testProvider{models: models}, nil
	})

	conf := &config.Config{
		Remotes: map[string]config.RemoteConfig{
			"gemini":   {Export: true},
			"gemini:1": {Type: "test_exported_group", Options: map[string]any{"slug": "alpha"}},
			"gemini:2": {Type: "test_exported_group", Options: map[string]any{"slug": "beta"}},
		},
	}

	resolve := NewResolver(writeTestConfig(t, conf))
	provider, err := resolve.Exported()
	if err != nil {
		t.Fatalf("Exported() error = %v", err)
	}

	models, err := provider.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Slug != "alpha" || models[1].Slug != "beta" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

// passthroughChatProvider emits a tool call the exported layer does not own,
// plus ResponseCompleted with StopReasonToolCall — the same shape clients rely
// on for multi-turn tool loops.
type passthroughChatProvider struct {
	models []Model
}

func (p *passthroughChatProvider) Name() string { return "passthrough" }

func (p *passthroughChatProvider) List(context.Context) ([]Model, error) { return p.models, nil }

func (p *passthroughChatProvider) Chat(context.Context, message.Request) (<-chan message.Event, error) {
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

func TestExportedChatWithToolsPreservesToolCallFinishReason(t *testing.T) {
	t.Parallel()

	backend := exportedBackend{
		name: "demo",
		provider: &passthroughChatProvider{
			models: []Model{{Slug: "demo-model", Name: "demo"}},
		},
	}
	// Non-empty tools force chatWithTools; toolMap empty so "exec" is passthrough.
	provider := &exportedProvider{
		backends: []exportedBackend{backend},
		models: map[string]exportedModel{
			"demo-model": {backend: backend, model: Model{Slug: "demo-model", Name: "demo"}},
		},
		tools: []tools.Tool{{
			Definition: message.ToolDefinition{Name: "owned_local_tool"},
		}},
		toolMap: map[string]tools.Tool{},
	}

	ch, err := provider.Chat(context.Background(), message.Request{Model: "demo-model"})
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

func writeTestConfig(t *testing.T, cfg *config.Config) *config.ConfigLoader {
	t.Helper()

	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	path := filepath.Join(t.TempDir(), "mclone.conf")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return &config.ConfigLoader{Location: path}
}
