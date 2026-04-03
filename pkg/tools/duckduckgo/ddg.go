package duckduckgo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	json "github.com/goccy/go-json"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/lucasew/mclone/pkg/tools"
	"golang.org/x/net/html"
)

var (
	urlRegex         = regexp.MustCompile(`uddg=([^&"]*)`)
	searchToolSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"The search query"}},"required":["query"]}`)
)

type ddgConfig struct {
	MaxResults int    `mapstructure:"max_results"`
	ToolName   string `mapstructure:"tool_name"`
}

type ddgSource struct {
	maxResults int
	toolName   string
}

func (s *ddgSource) Tools(ctx context.Context) ([]tools.Tool, error) {
	return []tools.Tool{{
		Definition: message.ToolDefinition{
			Type:        "function",
			Name:        s.toolName,
			Description: "Search the web for current information using DuckDuckGo. Use this when you need up-to-date data or facts you're not sure about.",
			Parameters:  searchToolSchema,
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			results, err := search(ctx, a.Query, s.maxResults)
			if err != nil {
				return fmt.Sprintf("Search error: %v", err), nil
			}
			return formatResults(results), nil
		},
	}}, nil
}

func search(ctx context.Context, query string, maxResults int) ([]searchResult, error) {
	searchURL := fmt.Sprintf("https://duckduckgo.com/html?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			monitor.ReportError(ctx, err, "action", "close_error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DDG returned status %d", resp.StatusCode)
	}

	return parseResults(resp.Body, maxResults)
}

type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

func parseResults(r io.Reader, maxResults int) ([]searchResult, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	var results []searchResult
	var traverse func(*html.Node)

	traverse = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}

		if n.Type == html.ElementNode && n.Data == "a" {
			var className, href string
			for _, a := range n.Attr {
				if a.Key == "class" {
					className = a.Val
				}
				if a.Key == "href" {
					href = a.Val
				}
			}

			if strings.Contains(className, "result__a") {
				title := extractText(n)
				link := href

				if matches := urlRegex.FindStringSubmatch(link); len(matches) > 1 {
					if decoded, err := url.QueryUnescape(matches[1]); err == nil {
						link = decoded
					}
				}

				snippet := ""
				if n.Parent != nil {
					for c := n.Parent.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == html.ElementNode && c.Data == "a" {
							for _, a := range c.Attr {
								if a.Key == "class" && strings.Contains(a.Val, "result__snippet") {
									snippet = extractText(c)
									break
								}
							}
						}
					}
				}

				if link != "" && !strings.Contains(link, "duckduckgo.com/y.js") {
					results = append(results, searchResult{
						Title:   title,
						URL:     link,
						Snippet: snippet,
					})
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(doc)

	return results, nil
}

func extractText(n *html.Node) string {
	var sb strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return strings.TrimSpace(sb.String())
}

func formatResults(results []searchResult) string {
	var sb strings.Builder
	for i, r := range results {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "[%d] %s\n%s\n%s", i+1, r.Title, r.URL, r.Snippet)
	}
	return sb.String()
}

func init() {
	tools.Register("duckduckgo", func(name string, options map[string]any) (tools.ToolSource, error) {
		var cfg ddgConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		if cfg.MaxResults == 0 {
			cfg.MaxResults = 5
		}
		toolName := cfg.ToolName
		if toolName == "" {
			toolName = "WebSearch"
		}
		return &ddgSource{maxResults: cfg.MaxResults, toolName: toolName}, nil
	})
}
