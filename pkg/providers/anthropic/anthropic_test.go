package anthropic

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lucasew/mclone/pkg/httpclient"
)

func TestHTTPClientsUseSharedPackage(t *testing.T) {
	t.Parallel()
	if listHTTPClient != httpclient.List {
		t.Fatal("listHTTPClient should be httpclient.List")
	}
	if streamHTTPClient != httpclient.Stream {
		t.Fatal("streamHTTPClient should be httpclient.Stream")
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
		if _, err := w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-20250514"},{"id":"claude-haiku-4-5-20251001"}]}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	p := &AnthropicProvider{BaseURL: srv.URL, APIKey: "test-key"}
	models, err := p.List(t.Context())
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
	_, err := p.List(t.Context())
	if err == nil {
		t.Fatal("List: want error for 401")
	}
	if !errors.Is(err, ErrListStatus) {
		t.Errorf("error = %v, want %v", err, ErrListStatus)
	}
}

func TestListInvalidJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`not-json`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	p := &AnthropicProvider{BaseURL: srv.URL, APIKey: "test-key"}
	_, err := p.List(t.Context())
	if err == nil {
		t.Fatal("List: want decode error, got nil")
	}
}
