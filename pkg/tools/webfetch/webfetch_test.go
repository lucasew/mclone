package webfetch

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseFetchURL(t *testing.T) {
	t.Parallel()

	okCases := []string{
		"https://example.com/article",
		"http://example.com/",
		"HTTPS://Example.COM/path?q=1",
	}
	for _, raw := range okCases {
		u, err := parseFetchURL(raw)
		if err != nil {
			t.Errorf("parseFetchURL(%q): unexpected error: %v", raw, err)
			continue
		}
		if u.Host == "" {
			t.Errorf("parseFetchURL(%q): empty host", raw)
		}
	}

	badCases := []struct {
		raw     string
		wantSub string
	}{
		{"file:///etc/passwd", "scheme"},
		{"gopher://example.com/1", "scheme"},
		{"//example.com/path", "scheme"},
		{"not-a-url", "scheme"},
		{"http:///no-host", "host"},
		{"https://", "host"},
	}
	for _, tc := range badCases {
		_, err := parseFetchURL(tc.raw)
		if err == nil {
			t.Errorf("parseFetchURL(%q): want error, got nil", tc.raw)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), tc.wantSub) {
			t.Errorf("parseFetchURL(%q): error %q should mention %q", tc.raw, err, tc.wantSub)
		}
	}
}

func TestIsBlockedIP(t *testing.T) {
	t.Parallel()

	blocked := []string{
		"127.0.0.1",
		"::1",
		"10.0.0.1",
		"192.168.1.1",
		"172.16.0.1",
		"169.254.169.254",
		"0.0.0.0",
		"::",
		"224.0.0.1",
		"ff02::1",
		"100.64.0.1",      // CGNAT (not IsPrivate in Go)
		"100.127.255.254", // CGNAT high end
		"198.18.0.1",      // benchmarking
		"192.0.2.1",       // TEST-NET-1
		"198.51.100.1",    // TEST-NET-2
		"203.0.113.1",     // TEST-NET-3
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("ParseIP(%q) failed", s)
		}
		if !isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s): want blocked", s)
		}
	}

	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"2001:4860:4860::8888",
	}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("ParseIP(%q) failed", s)
		}
		if isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s): want allowed", s)
		}
	}
}

func TestFetchAndParseRejectsNonHTTPScheme(t *testing.T) {
	t.Parallel()

	s := &webfetchSource{
		client:      plainClient(),
		maxBodySize: defaultMaxBodySize,
		toolName:    "WebFetch",
	}
	_, err := s.fetchAndParse(t.Context(), "file:///etc/passwd", "md")
	if err == nil {
		t.Fatal("fetchAndParse: want error for file:// URL, got nil")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Errorf("error %q should mention scheme", err)
	}
}

// plainClient is used in tests so httptest (loopback) is not blocked by the
// production SSRF dialer in newHTTPClient.
func plainClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func TestFetchAndParseNonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		if _, err := w.Write([]byte(`<!DOCTYPE html><html><body><h1>Not Found</h1><p>missing page content</p></body></html>`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	s := &webfetchSource{
		client:      plainClient(),
		maxBodySize: defaultMaxBodySize,
		toolName:    "WebFetch",
	}
	_, err := s.fetchAndParse(t.Context(), srv.URL+"/missing", "md")
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
		if _, err := w.Write([]byte(`internal error page with lots of html filler content`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	s := &webfetchSource{
		client:      plainClient(),
		maxBodySize: defaultMaxBodySize,
		toolName:    "WebFetch",
	}
	_, err := s.fetchAndParse(t.Context(), srv.URL+"/", "html")
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
		if _, err := w.Write([]byte(`<!DOCTYPE html><html><head><title>T</title></head><body><p>x</p></body></html>`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	s := &webfetchSource{
		client:      plainClient(),
		maxBodySize: defaultMaxBodySize,
		toolName:    "WebFetch",
	}
	_, err := s.fetchAndParse(t.Context(), srv.URL+"/", "html")
	if err != nil && strings.Contains(err.Error(), "status") {
		t.Fatalf("status gate should allow 200, got %v", err)
	}
	// err may be a parse/readability failure; that is fine for this test.
}

func TestSafeDialerBlocksCGNAT(t *testing.T) {
	t.Parallel()
	// 100.64.0.0/10 is not IsPrivate in Go; the dialer must still refuse it.
	client := newHTTPClient(2*time.Second, 1)
	req, err := http.NewRequest("GET", "http://100.64.0.1:80/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected error dialing CGNAT 100.64.0.1, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to connect to private network address") {
		t.Fatalf("error %q should mention refusing private network", err)
	}
}
