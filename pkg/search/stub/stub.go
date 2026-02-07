package stub

import (
	"context"
	"log/slog"

	"github.com/lucasew/mclone/pkg/remote"
)

type StubSearcher struct{}

func (s *StubSearcher) Search(ctx context.Context, query string, maxResults int) ([]remote.SearchResult, error) {
	slog.Warn("stub_search_called", "query", query, "max_results", maxResults)
	return []remote.SearchResult{{
		Title:   "Stub result",
		URL:     "https://example.com",
		Snippet: "Search not configured. Replace stub_search with a real search backend.",
	}}, nil
}

func init() {
	remote.RegisterSearcher("stub_search", func(name string, options map[string]string) (remote.Searcher, error) {
		return &StubSearcher{}, nil
	})
}
