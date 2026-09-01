package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListHTTPClientHasTimeout(t *testing.T) {
	t.Parallel()
	if listHTTPClient.Timeout != 30*time.Second {
		t.Fatalf("listHTTPClient.Timeout = %v, want 30s", listHTTPClient.Timeout)
	}
}

func TestStreamHTTPClientHasNoOverallTimeout(t *testing.T) {
	t.Parallel()
	// Overall Timeout would abort long SSE streams after the deadline.
	if streamHTTPClient.Timeout != 0 {
		t.Fatalf("streamHTTPClient.Timeout = %v, want 0 (unbounded body; use context)", streamHTTPClient.Timeout)
	}
	tr, ok := streamHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("streamHTTPClient.Transport type = %T, want *http.Transport", streamHTTPClient.Transport)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %v, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 60*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 60s", tr.ResponseHeaderTimeout)
	}
}

func TestListSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q, want 2023-06-01", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-20250514"},{"id":"claude-haiku-4-5-20251001"}]}`))
	}))
	t.Cleanup(srv.Close)

	p := &AnthropicProvider{BaseURL: srv.URL, APIKey: "test-key"}
	models, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].Name != "claude-sonnet-4-20250514" || models[0].Slug != "claude-sonnet-4-20250514" {
		t.Errorf("models[0] = %+v", models[0])
	}
	if models[1].Name != "claude-haiku-4-5-20251001" {
		t.Errorf("models[1].Name = %q", models[1].Name)
	}
}

func TestListNon2xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	p := &AnthropicProvider{BaseURL: srv.URL, APIKey: "bad"}
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatal("List: want error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q does not include status 401", err)
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

	p := &AnthropicProvider{BaseURL: srv.URL, APIKey: "test-key"}
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatal("List: want decode error, got nil")
	}
}
