package webfetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	json "github.com/goccy/go-json"

	"codeberg.org/readeck/go-readability/v2"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/lucasew/mclone/pkg/tools"
	"github.com/mattn/godown"
	"golang.org/x/net/html"
)

const (
	defaultMaxRedirects = 5
	defaultTimeout      = 30 * time.Second
	defaultMaxBodySize  = int64(2 * 1024 * 1024) // 2 MiB
)

// Package-level sentinels for errors.Is matching (including in tests).
var (
	ErrPrivateNetwork    = errors.New("refusing to connect to private network address")
	ErrMissingScheme     = errors.New("webfetch: URL must use http or https scheme")
	ErrUnsupportedScheme = errors.New("webfetch: unsupported URL scheme")
	ErrMissingHost       = errors.New("webfetch: URL missing host")
	ErrUnexpectedStatus  = errors.New("webfetch: unexpected status")
	ErrTooManyRedirects  = errors.New("webfetch: too many redirects")
)

var webFetchToolSchema = json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"The URL of the article or web page to fetch"},"format":{"type":"string","enum":["md","markdown","html","text","json"],"description":"The output format (default: md)"}},"required":["url"]}`)

type webfetchConfig struct {
	Timeout      string `mapstructure:"timeout"`
	MaxRedirects int    `mapstructure:"max_redirects"`
	MaxBodySize  int64  `mapstructure:"max_body_size"`
	ToolName     string `mapstructure:"tool_name"`
}

type webfetchSource struct {
	client      *http.Client
	maxBodySize int64
	toolName    string
}

func (s *webfetchSource) Tools(ctx context.Context) ([]tools.Tool, error) {
	return []tools.Tool{{
		Definition: message.ToolDefinition{
			Type:        "function",
			Name:        s.toolName,
			Description: "Fetch and parse the content of a web page/article. Use this to read external links provided by the user or found via search.",
			Parameters:  webFetchToolSchema,
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				URL    string `json:"url"`
				Format string `json:"format"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Format == "" {
				a.Format = "md"
			}
			return s.fetchAndParse(ctx, a.URL, a.Format)
		},
	}}, nil
}

// newHTTPClient creates an HTTP client with the given parameters.
func newHTTPClient(timeout time.Duration, maxRedirects int) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: newSafeDialer(timeout).DialContext,
		},
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%w: stopped after %d", ErrTooManyRedirects, maxRedirects)
			}
			return nil
		},
	}
}

// Special-use IPv4 ranges that Go's net.IP.IsPrivate does not cover, but that
// must not be reachable via SSRF (CGNAT, benchmarking, documentation).
var forbiddenIPv4Nets = []net.IPNet{
	// RFC 6598 — Shared Address Space (carrier-grade NAT / some VPN overlays)
	{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)},
	// RFC 2544 — Network Interconnect Device Benchmark Testing
	{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)},
	// RFC 5737 — documentation (TEST-NET-1/2/3)
	{IP: net.IPv4(192, 0, 2, 0), Mask: net.CIDRMask(24, 32)},
	{IP: net.IPv4(198, 51, 100, 0), Mask: net.CIDRMask(24, 32)},
	{IP: net.IPv4(203, 0, 113, 0), Mask: net.CIDRMask(24, 32)},
	// RFC 6890 — IETF protocol assignments
	{IP: net.IPv4(192, 0, 0, 0), Mask: net.CIDRMask(24, 32)},
}

func newSafeDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: timeout,
		// Control sees the resolved dial target as IP:port after DNS. Parse that
		// IP fail-closed (no second LookupIP that could diverge / rebind).
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if isBlockedIP(ip) {
				return ErrPrivateNetwork
			}
			return nil
		},
	}
}

// isBlockedIP reports whether dialing ip would allow SSRF into local,
// private, or special-use space (including CGNAT 100.64.0.0/10).
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// Normalize IPv4-mapped IPv6 so To4-based nets match.
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	// IsGlobalUnicast is false for loopback, link-local, multicast, unspecified.
	// It is still true for RFC1918/ULA and CGNAT, so those need extra checks.
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return true
	}
	for i := range forbiddenIPv4Nets {
		if forbiddenIPv4Nets[i].Contains(ip) {
			return true
		}
	}
	return false
}

// parseFetchURL requires an absolute http(s) URL with a host. Non-HTTP schemes
// (file, gopher, …) and scheme-relative URLs are rejected before dial.
func parseFetchURL(raw string) (*url.URL, error) {
	link, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(link.Scheme)
	if scheme != "http" && scheme != "https" {
		if link.Scheme == "" {
			return nil, ErrMissingScheme
		}
		return nil, fmt.Errorf("%w %q (only http and https)", ErrUnsupportedScheme, link.Scheme)
	}
	if link.Host == "" {
		return nil, ErrMissingHost
	}
	return link, nil
}

var userAgentPool = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36 Edg/134.0.0.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
}

func getRandomUserAgent() string {
	return userAgentPool[rand.Intn(len(userAgentPool))]
}

func (s *webfetchSource) fetchAndParse(ctx context.Context, rawLink string, format string) (string, error) {
	link, err := parseFetchURL(rawLink)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", link.String(), nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", getRandomUserAgent())
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	res, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := res.Body.Close(); cerr != nil {
			monitor.ReportError(ctx, cerr, "action", "webfetch_response_body_close")
		}
	}()

	// Non-2xx bodies are usually error pages; do not feed them to the
	// readability parser as if they were the requested article.
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("%w %d for %s", ErrUnexpectedStatus, res.StatusCode, link.String())
	}

	reader := io.LimitReader(res.Body, s.maxBodySize)

	node, err := html.Parse(reader)
	if err != nil {
		return "", err
	}

	parser := readability.NewParser()
	article, err := parser.ParseDocument(node, link)
	if err != nil {
		return "", err
	}

	contentBuf := &bytes.Buffer{}
	if err := article.RenderHTML(contentBuf); err != nil {
		return "", err
	}

	switch format {
	case "json":
		b, err := json.Marshal(map[string]string{
			"title":   article.Title(),
			"content": contentBuf.String(),
			"excerpt": article.Excerpt(),
			"byline":  article.Byline(),
		})
		if err != nil {
			return "", err
		}
		return string(b), nil
	case "html":
		return contentBuf.String(), nil
	default: // md, markdown, text, txt
		var out bytes.Buffer
		if err := godown.Convert(&out, contentBuf, nil); err != nil {
			return "", err
		}
		return out.String(), nil
	}
}

func init() {
	tools.Register("webfetch", func(name string, options map[string]any) (tools.ToolSource, error) {
		var cfg webfetchConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}

		timeout := defaultTimeout
		if cfg.Timeout != "" {
			d, err := time.ParseDuration(cfg.Timeout)
			if err != nil {
				return nil, fmt.Errorf("webfetch: invalid timeout %q: %w", cfg.Timeout, err)
			}
			timeout = d
		}

		maxRedirects := defaultMaxRedirects
		if cfg.MaxRedirects > 0 {
			maxRedirects = cfg.MaxRedirects
		}

		maxBody := defaultMaxBodySize
		if cfg.MaxBodySize > 0 {
			maxBody = cfg.MaxBodySize
		}

		toolName := cfg.ToolName
		if toolName == "" {
			toolName = "WebFetch"
		}

		return &webfetchSource{
			client:      newHTTPClient(timeout, maxRedirects),
			maxBodySize: maxBody,
			toolName:    toolName,
		}, nil
	})
}
