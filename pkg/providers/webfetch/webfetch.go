package webfetch

import (
	"bytes"
	"context"
	json "github.com/goccy/go-json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"codeberg.org/readeck/go-readability/v2"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/mattn/godown"
	"golang.org/x/net/html"
)

const (
	maxRedirects      = 5
	httpClientTimeout = 10 * time.Second
	maxBodySize       = int64(2 * 1024 * 1024) // 2 MiB
	dialerTimeout     = 30 * time.Second
	dialerKeepAlive   = 30 * time.Second
)

var webFetchToolSchema = json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"The URL of the article or web page to fetch"},"format":{"type":"string","enum":["md","markdown","html","text","json"],"description":"The output format (default: md)"}},"required":["url"]}`)

type WebFetchWrapperProvider struct {
	base remote.Provider
}

func (p *WebFetchWrapperProvider) Name() string { return "webfetch" }

func (p *WebFetchWrapperProvider) List(ctx context.Context) ([]remote.Model, error) {
	return p.base.List(ctx)
}

func (p *WebFetchWrapperProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	// Inject WebFetch function tool if not already present
	hasFetch := false
	for _, t := range options.Tools {
		if strings.EqualFold(t.Name, "WebFetch") {
			hasFetch = true
			break
		}
	}
	if !hasFetch {
		options.Tools = append(options.Tools, message.ToolDefinition{
			Type:        "function",
			Name:        "WebFetch",
			Description: "Fetch and parse the content of a web page/article. Use this to read external links provided by the user or found via search.",
			Parameters:  webFetchToolSchema,
		})
	}

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)

		// We need to intercept the response to handle tool calls
		// Since we don't know if the base provider supports streaming tool calls properly or if we need to
		// intercept them, we'll assume we wrap the chat loop similar to search.
		// However, search loop handles re-prompting. Here we also want to return the fetched content to the model.

		currentMsgs := make([]message.Message, len(messages))
		copy(currentMsgs, messages)

		// Using a loop to allow the model to use the tool and continue
		maxLoops := 5
		for loop := 0; loop < maxLoops; loop++ {
			ch, err := p.base.Chat(ctx, modelName, currentMsgs, options)
			if err != nil {
				out <- message.ChatResponse{Error: err}
				return
			}

			var assistantParts []message.Part
			var fetchCalls []message.ToolCall
			var otherCalls []message.ToolCall

			for resp := range ch {
				if resp.Error != nil {
					out <- resp
					return
				}
				if resp.Content != "" {
					out <- resp
					assistantParts = append(assistantParts, message.TextPart{Text: resp.Content})
				}
				if resp.Thought != "" {
					out <- resp
				}
				for _, tc := range resp.ToolCalls {
					if strings.EqualFold(tc.Name, "WebFetch") {
						fetchCalls = append(fetchCalls, tc)
					} else {
						otherCalls = append(otherCalls, tc)
					}
				}
			}

			// If no fetch calls, we are done with interception (unless there are other tools we don't handle)
			if len(fetchCalls) == 0 {
				if len(otherCalls) > 0 {
					out <- message.ChatResponse{ToolCalls: otherCalls}
				}
				out <- message.ChatResponse{Done: true}
				return
			}

			// Add assistant message with tool calls
			for _, tc := range fetchCalls {
				assistantParts = append(assistantParts, message.ToolCallPart{
					ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
			for _, tc := range otherCalls {
				assistantParts = append(assistantParts, message.ToolCallPart{
					ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
			currentMsgs = append(currentMsgs, message.Message{
				Role: message.RoleAssistant, Parts: assistantParts,
			})

			// Execute fetches
			for _, tc := range fetchCalls {
				var args struct {
					URL    string `json:"url"`
					Format string `json:"format"`
				}
				if err := json.Unmarshal(tc.Arguments, &args); err != nil {
					slog.Error("webfetch_arg_error", "error", err)
					currentMsgs = append(currentMsgs, message.Message{
						Role: message.RoleTool,
						Parts: []message.Part{message.ToolResultPart{
							ToolCallID: tc.ID,
							Content:    fmt.Sprintf("Error parsing arguments: %v", err),
						}},
					})
					continue
				}

				if args.Format == "" {
					args.Format = "md"
				}

				slog.Info("webfetch_executing", "url", args.URL, "format", args.Format)
				content, err := fetchAndParse(ctx, args.URL, args.Format)
				if err != nil {
					slog.Error("webfetch_error", "url", args.URL, "error", err)
					currentMsgs = append(currentMsgs, message.Message{
						Role: message.RoleTool,
						Parts: []message.Part{message.ToolResultPart{
							ToolCallID: tc.ID,
							Content:    fmt.Sprintf("Error fetching URL: %v", err),
						}},
					})
				} else {
					currentMsgs = append(currentMsgs, message.Message{
						Role: message.RoleTool,
						Parts: []message.Part{message.ToolResultPart{
							ToolCallID: tc.ID,
							Content:    content,
						}},
					})
				}
			}

			// Emit other tool calls to client if any
			if len(otherCalls) > 0 {
				out <- message.ChatResponse{ToolCalls: otherCalls}
			}

			// Loop continues to re-prompt the model with the tool output
		}

		out <- message.ChatResponse{Done: true}
	}()

	return out, nil
}

// HTTP Client Setup (copied/adapted from articleparser)

var httpClient = &http.Client{
	Transport: &http.Transport{
		DialContext: newSafeDialer().DialContext,
	},
	Timeout: httpClientTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	},
}

func newSafeDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   dialerTimeout,
		KeepAlive: dialerKeepAlive,
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
	// Simple rotation or random is fine
	return userAgentPool[time.Now().UnixNano()%int64(len(userAgentPool))]
}

func fetchAndParse(ctx context.Context, rawLink string, format string) (string, error) {
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

	res, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	reader := io.LimitReader(res.Body, maxBodySize)
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
	case "md", "markdown":
		var out bytes.Buffer
		godown.Convert(&out, contentBuf, nil)
		return out.String(), nil
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
	case "text", "txt":
		// Default to markdown for text since readability v2 doesn't expose clean text directly easily
		var out bytes.Buffer
		godown.Convert(&out, contentBuf, nil)
		return out.String(), nil
	case "html":
		return contentBuf.String(), nil
	default:
		// Default to markdown
		var out bytes.Buffer
		godown.Convert(&out, contentBuf, nil)
		return out.String(), nil
	}
}

func init() {
	remote.Register("webfetch", func(name string, options map[string]string, resolve remote.Resolver) (remote.Provider, error) {
		providerName := options["provider"]
		if providerName == "" {
			return nil, fmt.Errorf("webfetch wrapper requires 'provider' option")
		}

		base, err := resolve.Provider(providerName)
		if err != nil {
			return nil, fmt.Errorf("webfetch wrapper: failed to resolve provider %q: %w", providerName, err)
		}

		return &WebFetchWrapperProvider{
			base: base,
		}, nil
	})
}
