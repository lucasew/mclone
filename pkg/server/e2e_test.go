package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/config"
	"github.com/lucasew/mclone/pkg/message"
	_ "github.com/lucasew/mclone/pkg/providers/route"
	_ "github.com/lucasew/mclone/pkg/providers/toolbox"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/lucasew/mclone/pkg/tools"
	"net/http"
	"net/http/httptest"
)

type captureProvider struct {
	name string

	mu      sync.Mutex
	lastReq message.Request
}

func (p *captureProvider) Name() string { return "capturetest" }

func (p *captureProvider) List(ctx context.Context) ([]remote.Model, error) {
	return []remote.Model{{Slug: "real-model", Name: "real-model"}}, nil
}

func (p *captureProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	p.mu.Lock()
	p.lastReq = req
	p.mu.Unlock()

	ch := make(chan message.Event, 2)
	if len(req.Options.Tools) > 0 {
		toolName := req.Options.Tools[0].Name
		for _, tool := range req.Options.Tools {
			if tool.Name == "web_search" {
				toolName = tool.Name
				break
			}
		}
		ch <- message.ToolCallFinished{
			Call: message.ToolCall{
				ID:        "call_capture",
				Name:      toolName,
				Arguments: json.RawMessage(`{"query":"ping"}`),
			},
		}
		ch <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
	} else {
		ch <- message.TextDelta{Text: "ok"}
		ch <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
	}
	close(ch)
	return ch, nil
}

func (p *captureProvider) snapshot() message.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastReq
}

type staticToolSource struct {
	tool tools.Tool
}

func (s *staticToolSource) Tools(ctx context.Context) ([]tools.Tool, error) {
	return []tools.Tool{s.tool}, nil
}

var (
	registerE2EOnce sync.Once
	captureMu       sync.Mutex
	captures        = map[string]*captureProvider{}
)

func registerE2EProviders() {
	registerE2EOnce.Do(func() {
		remote.Register("capturetest", func(name string, options map[string]any, _ remote.Resolver) (remote.Provider, error) {
			p := &captureProvider{name: name}
			captureMu.Lock()
			captures[name] = p
			captureMu.Unlock()
			return p, nil
		})
		tools.Register("statictest", func(name string, options map[string]any) (tools.ToolSource, error) {
			toolName, _ := options["tool_name"].(string)
			if toolName == "" {
				toolName = name
			}
			return &staticToolSource{
				tool: tools.Tool{
					Definition: message.ToolDefinition{
						Type:        "function",
						Name:        toolName,
						Description: "static test tool",
						Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
					},
					Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
						return "unused", nil
					},
				},
			}, nil
		})
	})
}

func captureFor(name string) *captureProvider {
	captureMu.Lock()
	defer captureMu.Unlock()
	return captures[name]
}

func newTestServer(t *testing.T, toml string, remoteName string) (*Server, *captureProvider) {
	t.Helper()
	registerE2EProviders()

	dir := t.TempDir()
	confPath := filepath.Join(dir, "mclone.conf")
	if err := os.WriteFile(confPath, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loader := &config.ConfigLoader{Location: confPath}
	resolve := remote.NewResolver(loader)
	provider, err := resolve.Provider(remoteName)
	if err != nil {
		t.Fatalf("resolve provider %q: %v", remoteName, err)
	}

	return New(Config{Provider: provider}), captureFor("backend")
}

func postChatCompletion(t *testing.T, srv *Server, payload string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("content-type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return got
}

func TestRouteE2EPreservesIntermediateRequest(t *testing.T) {
	t.Parallel()

	srv, backend := newTestServer(t, `
[remotes.backend]
type = "capturetest"

[remotes.alias]
type = "route"

[remotes.alias.options]
claw = "backend:real-model"
`, "alias")

	resp := postChatCompletion(t, srv, `{
		"model":"claw",
		"stream":false,
		"messages":[{"role":"user","content":"ping"}],
		"tools":[{"type":"function","function":{"name":"web_search","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}]
	}`)

	req := backend.snapshot()
	if req.Model != "real-model" {
		t.Fatalf("expected routed model real-model, got %q", req.Model)
	}
	if len(req.Options.Tools) != 1 || req.Options.Tools[0].Name != "web_search" {
		t.Fatalf("expected one tool named web_search, got %#v", req.Options.Tools)
	}

	choices := resp["choices"].([]any)
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %#v", choice["finish_reason"])
	}
}

func TestToolboxRouteE2EPreservesIntermediateRequest(t *testing.T) {
	t.Parallel()

	srv, backend := newTestServer(t, `
[tools.web_search]
type = "statictest"

[tools.web_search.options]
tool_name = "local_search"

[remotes.backend]
type = "capturetest"

[remotes.alias]
type = "route"

[remotes.alias.options]
claw = "backend:real-model"

[remotes.assistant]
type = "toolbox"

[remotes.assistant.options]
provider = "alias"
tools = ["web_search"]
`, "assistant")

	resp := postChatCompletion(t, srv, `{
		"model":"claw",
		"stream":false,
		"messages":[{"role":"user","content":"ping"}],
		"tools":[{"type":"function","function":{"name":"web_search","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}]
	}`)

	req := backend.snapshot()
	if req.Model != "real-model" {
		t.Fatalf("expected routed model real-model, got %q", req.Model)
	}
	if len(req.Options.Tools) != 2 {
		t.Fatalf("expected local tool injection plus passthrough tool, got %#v", req.Options.Tools)
	}
	var sawPassthrough bool
	for _, tool := range req.Options.Tools {
		if tool.Name == "web_search" {
			sawPassthrough = true
		}
	}
	if !sawPassthrough {
		t.Fatalf("expected passthrough tool web_search in %#v", req.Options.Tools)
	}

	choices := resp["choices"].([]any)
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %#v", choice["finish_reason"])
	}
}
