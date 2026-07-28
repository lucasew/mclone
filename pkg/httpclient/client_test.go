package httpclient

import (
	"net/http"
	"testing"
	"time"
)

func TestListHasTimeout(t *testing.T) {
	t.Parallel()
	if List.Timeout != 30*time.Second {
		t.Fatalf("List.Timeout = %v, want 30s", List.Timeout)
	}
}

func TestStreamHasNoOverallTimeout(t *testing.T) {
	t.Parallel()
	// Overall Timeout would abort long SSE streams after the deadline.
	if Stream.Timeout != 0 {
		t.Fatalf("Stream.Timeout = %v, want 0 (unbounded body; use context)", Stream.Timeout)
	}
	if Stream.Transport != StreamTransport {
		t.Fatalf("Stream.Transport is not StreamTransport")
	}
	tr, ok := Stream.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Stream.Transport type = %T, want *http.Transport", Stream.Transport)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Fatalf("TLSHandshakeTimeout = %v, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 60*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %v, want 60s", tr.ResponseHeaderTimeout)
	}
}
