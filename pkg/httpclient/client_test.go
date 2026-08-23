package httpclient

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

var errListStatus = errors.New("list models: unexpected status")
var errBodyBoom = errors.New("boom")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBodyBoom }
func (errReader) Close() error             { return nil }

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

func TestStatusErrorNilOn2xx(t *testing.T) {
	t.Parallel()
	for _, code := range []int{200, 204, 299} {
		resp := &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("ignored"))}
		if err := StatusError(resp, errListStatus, "list models"); err != nil {
			t.Fatalf("status %d: %v", code, err)
		}
	}
}

func TestStatusErrorWrapsBody(t *testing.T) {
	t.Parallel()
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("  invalid_api_key\n")),
	}
	err := StatusError(resp, errListStatus, "list models")
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, errListStatus) {
		t.Fatalf("error = %v, want %v", err, errListStatus)
	}
	want := "list models: unexpected status 401: invalid_api_key"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestStatusErrorEmptyBody(t *testing.T) {
	t.Parallel()
	resp := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("  \n"))}
	err := StatusError(resp, errListStatus, "list models")
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, errListStatus) {
		t.Fatalf("error = %v, want %v", err, errListStatus)
	}
	if err.Error() != "list models: unexpected status 502" {
		t.Fatalf("error = %q", err)
	}
}

func TestStatusErrorReadFailure(t *testing.T) {
	t.Parallel()
	resp := &http.Response{StatusCode: http.StatusInternalServerError, Body: errReader{}}
	err := StatusError(resp, errListStatus, "openai list models")
	if err == nil {
		t.Fatal("want error")
	}
	if errors.Is(err, errListStatus) {
		t.Fatalf("read failure should not wrap sentinel: %v", err)
	}
	if !errors.Is(err, errBodyBoom) {
		t.Fatalf("error = %v, want %v", err, errBodyBoom)
	}
	want := "openai list models: status 500: read body: boom"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}
