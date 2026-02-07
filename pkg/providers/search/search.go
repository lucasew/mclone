package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
)

var searchToolSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"The search query"}},"required":["query"]}`)

type SearchWrapperProvider struct {
	base       remote.Provider
	searcher   remote.Searcher
	maxResults int
	maxLoops   int
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

		for loop := 0; loop < p.maxLoops; loop++ {
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
				json.Unmarshal(tc.Arguments, &args)

				slog.Info("search_executing", "query", args.Query, "loop", loop)
				results, err := p.searcher.Search(ctx, args.Query, p.maxResults)
				if err != nil {
					slog.Error("search_error", "query", args.Query, "error", err)
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
		}

		// Max loops reached
		slog.Warn("search_max_loops", "max", p.maxLoops)
		out <- message.ChatResponse{Done: true}
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
	remote.Register("search", func(name string, options map[string]string, resolve remote.Resolver) (remote.Provider, error) {
		providerName := options["provider"]
		if providerName == "" {
			return nil, fmt.Errorf("search wrapper requires 'provider' option")
		}
		searchName := options["search"]
		if searchName == "" {
			searchName = "ddg"
		}

		base, err := resolve.Provider(providerName)
		if err != nil {
			return nil, fmt.Errorf("search wrapper: failed to resolve provider %q: %w", providerName, err)
		}

		searcher, err := resolve.Searcher(searchName)
		if err != nil {
			return nil, fmt.Errorf("search wrapper: failed to resolve searcher %q: %w", searchName, err)
		}

		maxResults := 5
		if v, ok := options["max_results"]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				maxResults = n
			}
		}

		maxLoops := 3
		if v, ok := options["max_loops"]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				maxLoops = n
			}
		}

		return &SearchWrapperProvider{
			base:       base,
			searcher:   searcher,
			maxResults: maxResults,
			maxLoops:   maxLoops,
		}, nil
	})
}
