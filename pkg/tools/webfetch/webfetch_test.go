package webfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// plainClient is used in tests so httptest (loopback) is not blocked by the
// production SSRF dialer in newHTTPClient.
func plainClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func TestFetchAndParseNonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><h1>Not Found</h1><p>missing page content</p></body></html>`))
	}))
	t.Cleanup(srv.Close)

	s := &webfetchSource{
		client:      plainClient(),
		maxBodySize: defaultMaxBodySize,
		toolName:    "WebFetch",
	}
	_, err := s.fetchAndParse(context.Background(), srv.URL+"/missing", "md")
	if err == nil {
		t.Fatal("fetchAndParse: want error for non-2xx status, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q does not include status 404", err)
	}
	if !strings.Contains(err.Error(), "status") {
		t.Errorf("error %q should mention status", err)
	}
}

func TestFetchAndParseServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`internal error page with lots of html filler content`))
	}))
	t.Cleanup(srv.Close)

	s := &webfetchSource{
		client:      plainClient(),
		maxBodySize: defaultMaxBodySize,
		toolName:    "WebFetch",
	}
	_, err := s.fetchAndParse(context.Background(), srv.URL+"/", "html")
	if err == nil {
		t.Fatal("fetchAndParse: want error for 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not include status 500", err)
	}
}

func TestFetchAndParseOKPassesStatusGate(t *testing.T) {
	t.Parallel()

	// Minimal HTML; readability may still fail, but the status gate must not.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>T</title></head><body><p>x</p></body></html>`))
	}))
	t.Cleanup(srv.Close)

	s := &webfetchSource{
		client:      plainClient(),
		maxBodySize: defaultMaxBodySize,
		toolName:    "WebFetch",
	}
	_, err := s.fetchAndParse(context.Background(), srv.URL+"/", "html")
	if err != nil && strings.Contains(err.Error(), "status") {
		t.Fatalf("status gate should allow 200, got %v", err)
	}
	// err may be a parse/readability failure; that is fine for this test.
}
