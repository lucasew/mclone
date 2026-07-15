package antigravity

import (
	"net/http"
	"testing"
	"time"
)

func TestHTTPClientHasTimeout(t *testing.T) {
	if httpClient.Timeout != 30*time.Second {
		t.Fatalf("httpClient.Timeout = %v, want 30s", httpClient.Timeout)
	}
}

func TestStreamHTTPClientHasNoOverallTimeout(t *testing.T) {
	// Overall Timeout would abort long SSE streams after the deadline.
	if streamHTTPClient.Timeout != 0 {
		t.Fatalf("streamHTTPClient.Timeout = %v, want 0 (unbounded body; use context)", streamHTTPClient.Timeout)
	}
	tr, ok := streamHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("streamHTTPClient.Transport type = %T, want *http.Transport", streamHTTPClient.Transport)
	}
	if tr.ResponseHeaderTimeout == 0 {
		t.Fatal("streamHTTPClient ResponseHeaderTimeout must be set to bound hung headers")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Fatal("streamHTTPClient TLSHandshakeTimeout must be set")
	}
}
