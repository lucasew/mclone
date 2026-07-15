package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lucasew/mclone/pkg/message"
)

func TestListSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3","size":123},{"name":"mistral","size":456}]}`))
	}))
	t.Cleanup(srv.Close)

	p := &OllamaProvider{BaseURL: srv.URL}
	models, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].Name != "llama3" || models[0].Slug != "llama3" {
		t.Errorf("models[0] = %+v, want name/slug llama3", models[0])
	}
	if models[1].Name != "mistral" {
		t.Errorf("models[1].Name = %q, want mistral", models[1].Name)
	}
}

func TestListNonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`internal error`))
	}))
	t.Cleanup(srv.Close)

	p := &OllamaProvider{BaseURL: srv.URL}
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatal("List: want error for non-200 status, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not include status 500", err)
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Errorf("error should not be a JSON decode failure, got %q", err)
	}
}

func TestListInvalidJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not-json`))
	}))
	t.Cleanup(srv.Close)

	p := &OllamaProvider{BaseURL: srv.URL}
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatal("List: want decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error %q should mention decode", err)
	}
}

func TestListRequestError(t *testing.T) {
	t.Parallel()

	// Control characters in the URL make NewRequestWithContext fail.
	p := &OllamaProvider{BaseURL: "http://example.com/\x00"}
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatal("List: want request construction error, got nil")
	}
	if !strings.Contains(err.Error(), "request") {
		t.Errorf("error %q should mention request construction", err)
	}
}

func TestToSDKMessagesToolResultOrder(t *testing.T) {
	t.Parallel()

	// openai-go ToolMessage is (content, toolCallID). Swapping them puts the
	// tool call id in the content field and breaks multi-turn tool loops.
	const callID = "call_abc123"
	const content = `{"temp": 72}`

	msgs := toSDKMessages([]message.Turn{
		{
			Role: message.RoleTool,
			Parts: []message.Part{
				message.ToolResultPart{ToolCallID: callID, Content: content},
			},
		},
		{
			Role: message.RoleUser,
			Parts: []message.Part{
				message.ToolResultPart{ToolCallID: callID, Content: content},
			},
		},
	})
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	for i, m := range msgs {
		if m.OfTool == nil {
			t.Fatalf("msgs[%d]: expected tool message, got %#v", i, m)
		}
		if m.OfTool.ToolCallID != callID {
			t.Errorf("msgs[%d].ToolCallID = %q, want %q", i, m.OfTool.ToolCallID, callID)
		}
		if !m.OfTool.Content.OfString.Valid() {
			t.Fatalf("msgs[%d]: content OfString not set", i)
		}
		if got := m.OfTool.Content.OfString.Or(""); got != content {
			t.Errorf("msgs[%d].Content = %q, want %q", i, got, content)
		}
	}
}
