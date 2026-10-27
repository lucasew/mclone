package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

var ErrSecretBackendDetail = errors.New("secret backend detail: token=xyz")

type errListProvider struct{}

func (errListProvider) Name() string { return "err" }
func (errListProvider) List(context.Context) ([]remote.Model, error) {
	return nil, ErrSecretBackendDetail
}
func (errListProvider) Chat(context.Context, message.Request) (<-chan message.Event, error) {
	return nil, ErrSecretBackendDetail
}

func TestServeModelsDoesNotLeakInternalError(t *testing.T) {
	s := New(Config{Provider: errListProvider{}})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	s.serveModels(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "token=xyz") || strings.Contains(body, "secret backend") {
		t.Fatalf("leaked internal error: %q", body)
	}
	if !strings.Contains(strings.ToLower(body), "internal") {
		t.Fatalf("body=%q want internal server error", body)
	}
}
