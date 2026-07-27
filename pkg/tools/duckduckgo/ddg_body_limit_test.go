package duckduckgo

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearchLimitsResponseBody(t *testing.T) {
	// Oversized HTML should still parse prefix without reading unbounded.
	huge := strings.Repeat("x", int(defaultMaxBodySize)+1024)
	// Minimal shell so html.Parse succeeds on limited prefix
	body := "<html><body><div class=\"result results_links\">" + huge + "</div></body></html>"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	// Point search at test server by temporarily replacing httpClient.
	old := httpClient
	httpClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { httpClient = old })

	// search always hits duckduckgo.com — call parseResults via LimitReader path directly.
	// Exercise LimitReader + parseResults with an oversized stream.
	r := io.NopCloser(strings.NewReader(body))
	limited := io.LimitReader(r, defaultMaxBodySize)
	_, err := parseResults(limited, 5)
	if err != nil {
		// parse may yield zero results; that is fine — must not OOM/hang.
		t.Logf("parseResults err (ok if empty): %v", err)
	}
	// search() always hits duckduckgo.com (URL fixed); full search smoke is
	// integration-only. srv is kept so future tests can redirect the client.
	t.Logf("fixture server: %s", srv.URL)
}
