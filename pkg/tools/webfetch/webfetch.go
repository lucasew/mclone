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
	"syscall"
	"time"

	json "github.com/goccy/go-json"

	"codeberg.org/readeck/go-readability/v2"
	"github.com/lucasew/mclone/pkg/message"
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

var webFetchToolSchema = json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"The URL of the article or web page to fetch"},"format":{"type":"string","enum":["md","markdown","html","text","json"],"description":"The output format (default: md)"}},"required":["url"]}`)

type webfetchConfig struct {
	Timeout      string `mapstructure:"timeout"`
	MaxRedirects int    `mapstructure:"max_redirects"`
	MaxBodySize  int64  `mapstructure:"max_body_size"`
}

type webfetchSource struct {
	client      *http.Client
	maxBodySize int64
}

func (s *webfetchSource) Tools(ctx context.Context) ([]tools.Tool, error) {
	return []tools.Tool{{
		Definition: message.ToolDefinition{
			Type:        "function",
			Name:        "WebFetch",
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
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

func newSafeDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: timeout,
		Control: func(network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ips, err := net.LookupIP(host)
			if err != nil {
				return err
			}
			for _, ip := range ips {
				if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
					return errors.New("refusing to connect to private network address")
				}
			}
			return nil
		},
	}
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
	link, err := url.Parse(rawLink)
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
	defer res.Body.Close()

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

		return &webfetchSource{
			client:      newHTTPClient(timeout, maxRedirects),
			maxBodySize: maxBody,
		}, nil
	})
}
