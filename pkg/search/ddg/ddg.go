package ddg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/lucasew/mclone/pkg/remote"
	"golang.org/x/net/html"
)

type DDGSearcher struct{}

var urlRegex = regexp.MustCompile(`uddg=([^&"]*)`)

func (s *DDGSearcher) Search(ctx context.Context, query string, maxResults int) ([]remote.SearchResult, error) {
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DDG returned status %d", resp.StatusCode)
	}

	return parseResults(resp.Body, maxResults)
}

func parseResults(r io.Reader, maxResults int) ([]remote.SearchResult, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	var results []remote.SearchResult
	var traverse func(*html.Node)

	// We are looking for structure like:
	// <div class="result">
	//   <a class="result__a" href="...">Title</a>
	//   <a class="result__snippet" href="...">Snippet</a>
	// </div>
	// But in the 'html' version (lite), it's more like:
	// <div class="links_main links_deep result__body">
	//   <a class="result__a" href="...">Title</a>
	//   <a class="result__snippet" href="...">Snippet</a>

	traverse = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}

		// Basic heuristic: look for <a> tags with class "result__a" (Title + Link)
		// and verify if they have the 'uddg' param or are direct links
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
				// Found a potential result
				title := extractText(n)
				link := href

				// Decode the uddg param if present
				if matches := urlRegex.FindStringSubmatch(link); len(matches) > 1 {
					if decoded, err := url.QueryUnescape(matches[1]); err == nil {
						link = decoded
					}
				}

				// Look for snippet in siblings
				snippet := ""
				// Traverse siblings to find snippet
				// In lite HTML, snippet is often in a div with class "result__snippet"
				// But let's just grab the next few text nodes or look for a specific class in siblings
				// This is tricky without a full selector library like goquery or cascadia.
				// However, I just added cascadia as a dependency of go-readability!
				// I can import it? No, it's indirect.
				// I'll use a simple sibling search.

				// Basic implementation: just use title and link for now to match 'pesquisarr'
				// 'pesquisarr' only extracted the link.

				// To provide a snippet, let's search for the snippet element in the parent's children
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

				if link != "" {
					results = append(results, remote.SearchResult{
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

func init() {
	remote.RegisterSearcher("ddg", func(name string, options map[string]any) (remote.Searcher, error) {
		return &DDGSearcher{}, nil
	})
}
