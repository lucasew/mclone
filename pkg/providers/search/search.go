package search

import (
	"context"
	"fmt"
	json "github.com/goccy/go-json"
	"log/slog"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/remote"
)

var searchToolSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"The search query"}},"required":["query"]}`)

type SearchWrapperProvider struct {
	base       remote.Provider
	searcher   remote.Searcher
	maxResults int
	maxLoops   int
}

type SearchConfig struct {
	Provider   string `mapstructure:"provider"`
	Search     string `mapstructure:"search"`
	MaxResults int    `mapstructure:"max_results"`
	MaxLoops   int    `mapstructure:"max_loops"`
}

func (p *SearchWrapperProvider) Name() string { return "search" }

func (p *SearchWrapperProvider) List(ctx context.Context) ([]remote.Model, error) {
	return p.base.List(ctx)
}

func (p *SearchWrapperProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	// Remove existing web_search/WebSearch tool if present to avoid duplicates
	// and ensure we control the definition.
	var cleanTools []message.ToolDefinition
	for _, t := range options.Tools {
		if !strings.EqualFold(t.Name, "WebSearch") {
			cleanTools = append(cleanTools, t)
		}
	}
	options.Tools = cleanTools

	// Inject WebSearch function tool
	options.Tools = append(options.Tools, message.ToolDefinition{
		Type:        "function",
		Name:        "WebSearch",
		Description: "Search the web for current information. Use this when you need up-to-date data or facts you're not sure about.",
		Parameters:  searchToolSchema,
	})

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)
		currentMsgs := make([]message.Message, len(messages))
		copy(currentMsgs, messages)

		loop := 0
		for {
			if loop >= p.maxLoops {
				slog.Warn("search_max_loops", "max", p.maxLoops)
				out <- message.ChatResponse{Done: true}
				return
			}

			ch, err := p.base.Chat(ctx, modelName, currentMsgs, options)
			if err != nil {
				out <- message.ChatResponse{Error: err}
				return
			}

			// Consume stream: forward content/thoughts, buffer tool calls
			var assistantParts []message.Part
			var searchCalls []message.ToolCall
			var otherCalls []message.ToolCall

			for resp := range ch {
				if resp.Error != nil {
					out <- resp
					return
				}
				if resp.Content != "" {
					out <- resp // stream text to client
					assistantParts = append(assistantParts, message.TextPart{Text: resp.Content})
				}
				if resp.Thought != "" {
					out <- resp
				}
				for _, tc := range resp.ToolCalls {
					if strings.EqualFold(tc.Name, "WebSearch") {
						searchCalls = append(searchCalls, tc)
					} else {
						otherCalls = append(otherCalls, tc)
					}
				}
			}

			// No search calls — emit remaining tool calls + done
			if len(searchCalls) == 0 {
				if len(otherCalls) > 0 {
					out <- message.ChatResponse{ToolCalls: otherCalls}
				}
				out <- message.ChatResponse{Done: true}
				return
			}

			// Build assistant message with tool call parts
			for _, tc := range searchCalls {
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

			// Execute searches and append tool results
			for _, tc := range searchCalls {
				var args struct {
					Query string `json:"query"`
				}
				if err := json.Unmarshal(tc.Arguments, &args); err != nil {
					monitor.ReportError(ctx, err, "action", "search_args_error")
				}

				slog.Info("search_executing", "query", args.Query, "loop", loop)
				results, err := p.searcher.Search(ctx, args.Query, p.maxResults)
				if err != nil {
					monitor.ReportError(ctx, err, "action", "search_error", "query", args.Query)
					results = []remote.SearchResult{{
						Title:   "Search error",
						Snippet: err.Error(),
					}}
				}

				resultText := formatResults(results)
				currentMsgs = append(currentMsgs, message.Message{
					Role: message.RoleTool,
					Parts: []message.Part{message.ToolResultPart{
						ToolCallID: tc.ID,
						Content:    resultText,
					}},
				})
			}

			// Emit other tool calls to the client
			if len(otherCalls) > 0 {
				out <- message.ChatResponse{ToolCalls: otherCalls}
			}

			// Next loop: re-call provider with expanded history
			slog.Info("search_requery", "loop", loop+1, "search_calls", len(searchCalls))
			loop++
		}
	}()
	return out, nil
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
	remote.Register("search", func(name string, options map[string]any, resolve remote.Resolver) (remote.Provider, error) {
		var cfg SearchConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}

		if cfg.Provider == "" {
			return nil, fmt.Errorf("search wrapper requires 'provider' option")
		}
		if cfg.Search == "" {
			cfg.Search = "ddg"
		}
		if cfg.MaxResults == 0 {
			cfg.MaxResults = 5
		}
		if cfg.MaxLoops == 0 {
			cfg.MaxLoops = 3
		}

		base, err := resolve.Provider(cfg.Provider)
		if err != nil {
			return nil, fmt.Errorf("search wrapper: failed to resolve provider %q: %w", cfg.Provider, err)
		}

		searcher, err := resolve.Searcher(cfg.Search)
		if err != nil {
			return nil, fmt.Errorf("search wrapper: failed to resolve searcher %q: %w", cfg.Search, err)
		}

		return &SearchWrapperProvider{
			base:       base,
			searcher:   searcher,
			maxResults: cfg.MaxResults,
			maxLoops:   cfg.MaxLoops,
		}, nil
	})
}
