package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShouldIgnoreStreamError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		sawContent  bool
		sawToolCall bool
		want        bool
	}{
		{
			name:       "no error",
			err:        nil,
			sawContent: true,
			want:       false,
		},
		{
			name: "no output yet",
			err:  errors.New("stream error: stream ID 41; NO_ERROR; received from peer"),
			want: false,
		},
		{
			name:       "ignore no_error after content",
			err:        errors.New("stream error: stream ID 41; NO_ERROR; received from peer"),
			sawContent: true,
			want:       true,
		},
		{
			name:        "ignore context canceled after tool call",
			err:         errors.New("context canceled"),
			sawToolCall: true,
			want:        true,
		},
		{
			name:       "other stream errors still fail",
			err:        errors.New("stream error: unexpected EOF"),
			sawContent: true,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldIgnoreStreamError(tt.err, tt.sawContent, tt.sawToolCall)
			if got != tt.want {
				t.Fatalf("shouldIgnoreStreamError(%v, %v, %v) = %v, want %v", tt.err, tt.sawContent, tt.sawToolCall, got, tt.want)
			}
		})
	}
}

func TestListSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o","owned_by":"openai"},{"id":"o1-mini","owned_by":""}]}`))
	}))
	t.Cleanup(srv.Close)

	p := &OpenAIProvider{BaseURL: srv.URL, APIKey: "test-key"}
	models, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].Name != "gpt-4o" || models[0].Slug != "gpt-4o" {
		t.Errorf("models[0] = %+v, want Name/Slug gpt-4o", models[0])
	}
	if len(models[0].OwnedBy) != 1 || models[0].OwnedBy[0] != "openai" {
		t.Errorf("models[0].OwnedBy = %v, want [openai]", models[0].OwnedBy)
	}
	if models[1].OwnedBy != nil {
		t.Errorf("models[1].OwnedBy = %v, want nil", models[1].OwnedBy)
	}
}

func TestListNon2xx(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid_api_key"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	p := &OpenAIProvider{BaseURL: srv.URL, APIKey: "bad"}
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatal("List: want error for 401")
	}
	if !strings.Contains(err.Error(), "status 401") {
		t.Errorf("error = %q, want status 401", err)
	}
	if !strings.Contains(err.Error(), "invalid_api_key") {
		t.Errorf("error = %q, want body snippet", err)
	}
}
