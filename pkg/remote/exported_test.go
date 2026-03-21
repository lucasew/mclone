package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/message"
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
