package gemini

import (
	"net/http"
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
