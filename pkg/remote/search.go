package remote

import (
	"context"
	"fmt"
)

// SearchResult represents a single search result.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Searcher can execute web searches.
type Searcher interface {
	Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
}

// SearchFactory creates a Searcher from config options.
type SearchFactory func(name string, options map[string]string) (Searcher, error)

var searchRegistry = make(map[string]SearchFactory)

// RegisterSearcher registers a search backend type.
func RegisterSearcher(typeName string, factory SearchFactory) {
	searchRegistry[typeName] = factory
}

// NewSearcher creates a Searcher by type name.
func NewSearcher(typeName, name string, options map[string]string) (Searcher, error) {
	factory, ok := searchRegistry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown search type: %s", typeName)
	}
	return factory(name, options)
}
