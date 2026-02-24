package remote

import (
	"context"
	"fmt"
)

// SearchResult represents a single item returned from a search query.
// It normalizes results from different search providers (e.g. Google, Bing) into a common format.
type SearchResult struct {
	Title   string // The title of the search result page
	URL     string // The direct link to the result
	Snippet string // A brief text snippet or summary of the content
}

// Searcher is the interface for executing web searches.
// Implementations wrap specific search engine APIs.
type Searcher interface {
	// Search executes a query and returns up to maxResults items.
	// It should return an error if the backend is unreachable or the query fails.
	Search(ctx context.Context, query string, maxResults int) ([]SearchResult, error)
}

// SearchFactory is a function type for creating new Searcher instances.
// It receives the name of the search remote and its configuration options.
type SearchFactory func(name string, options map[string]any) (Searcher, error)

var searchRegistry = make(map[string]SearchFactory)

// RegisterSearcher registers a search backend type (e.g., "google", "ddg").
// The typeName matches the "type" field in the configuration.
func RegisterSearcher(typeName string, factory SearchFactory) {
	searchRegistry[typeName] = factory
}

// NewSearcher creates a new Searcher instance by its type name.
// It looks up the factory in the registry and instantiates the searcher.
func NewSearcher(typeName, name string, options map[string]any) (Searcher, error) {
	factory, ok := searchRegistry[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown search type: %s", typeName)
	}
	return factory(name, options)
}
