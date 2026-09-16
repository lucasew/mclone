package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

type listOnlyProvider struct {
	name   string
	models []remote.Model
}

func (p *listOnlyProvider) Name() string { return p.name }

func (p *listOnlyProvider) List(context.Context) ([]remote.Model, error) { return p.models, nil }

func (p *listOnlyProvider) Chat(context.Context, message.Request) (<-chan message.Event, error) {
	ch := make(chan message.Event)
	close(ch)
	return ch, nil
}

func TestModelsEndpointUsesOwnedByFromModel(t *testing.T) {
	t.Parallel()

	srv := New(Config{
		Provider: &listOnlyProvider{
			name: "fallback-provider",
			models: []remote.Model{
				{Slug: "alpha", Name: "alpha", OwnedBy: []string{"backend-a", "backend-b"}},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected one model, got %d", len(resp.Data))
	}
	if resp.Data[0].ID != "alpha" {
		t.Fatalf("expected model alpha, got %q", resp.Data[0].ID)
	}
	if resp.Data[0].OwnedBy != "backend-a,backend-b" {
		t.Fatalf("expected owned_by backend list, got %q", resp.Data[0].OwnedBy)
	}
}

func TestModelsEndpointFallsBackToProviderName(t *testing.T) {
	t.Parallel()

	srv := New(Config{
		Provider: &listOnlyProvider{
			name:   "fallback-provider",
			models: []remote.Model{{Slug: "alpha", Name: "alpha"}},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Data []struct {
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected one model, got %d", len(resp.Data))
	}
	if resp.Data[0].OwnedBy != "fallback-provider" {
		t.Fatalf("expected owned_by from provider name, got %q", resp.Data[0].OwnedBy)
	}
}
