package websearch

import (
	"context"
	"fmt"
	"strings"

	json "github.com/goccy/go-json"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/lucasew/mclone/pkg/tools"
)

var searchToolSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"The search query"}},"required":["query"]}`)

type websearchSource struct {
	searcher   remote.Searcher
	maxResults int
	toolName   string
}

type websearchConfig struct {
	Searcher   string `mapstructure:"searcher"`
	MaxResults int    `mapstructure:"max_results"`
	ToolName   string `mapstructure:"tool_name"`
}

func (s *websearchSource) Tools(ctx context.Context) ([]tools.Tool, error) {
	return []tools.Tool{{
		Definition: message.ToolDefinition{
			Type:        "function",
			Name:        s.toolName,
			Description: "Search the web for current information. Use this when you need up-to-date data or facts you're not sure about.",
			Parameters:  searchToolSchema,
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			results, err := s.searcher.Search(ctx, a.Query, s.maxResults)
			if err != nil {
				return fmt.Sprintf("Search error: %v", err), nil
			}
			return formatResults(results), nil
		},
	}}, nil
}

func formatResults(results []remote.SearchResult) string {
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
	tools.Register("websearch", func(name string, options map[string]any) (tools.ToolSource, error) {
		var cfg websearchConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		if cfg.Searcher == "" {
			cfg.Searcher = "ddg"
		}
		if cfg.MaxResults == 0 {
			cfg.MaxResults = 5
		}
		searcher, err := remote.NewSearcher(cfg.Searcher, name, nil)
		if err != nil {
			return nil, fmt.Errorf("websearch tool: unknown searcher %q: %w", cfg.Searcher, err)
		}
		toolName := cfg.ToolName
		if toolName == "" {
			toolName = "WebSearch"
		}
		return &websearchSource{searcher: searcher, maxResults: cfg.MaxResults, toolName: toolName}, nil
	})
}
