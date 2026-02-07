package anthropic

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type AnthropicProvider struct {
	BaseURL string
	APIKey  string
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) List(ctx context.Context) ([]remote.Model, error) {
	url := "https://api.anthropic.com/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]remote.Model, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, remote.Model{Name: m.ID, Slug: m.ID})
	}
	return models, nil
}

func (p *AnthropicProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	opts := []option.RequestOption{option.WithAPIKey(p.APIKey)}
	if p.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(p.BaseURL))
	}
	client := sdk.NewClient(opts...)

	params := sdk.MessageNewParams{
		Model:    sdk.Model(modelName),
		Messages: toSDKMessages(messages),
	}

	// System prompt
	if sys := extractSystem(messages); sys != "" {
		params.System = []sdk.TextBlockParam{{Text: sys}}
	}

	// Generation params
	if options.MaxTokens != nil {
		params.MaxTokens = int64(*options.MaxTokens)
	} else {
		params.MaxTokens = 8192
	}
	if options.Temperature != nil {
		params.Temperature = sdk.Float(*options.Temperature)
	}
	if options.TopP != nil {
		params.TopP = sdk.Float(*options.TopP)
	}
	if len(options.Stop) > 0 {
		params.StopSequences = options.Stop
	}

	// Tools
	if len(options.Tools) > 0 {
		params.Tools = toSDKTools(options.Tools)
		slog.Info("anthropic_tools_sent", "count", len(params.Tools), "names", listToolNames(params.Tools))
	}

	stream := client.Messages.NewStreaming(ctx, params)

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)
		defer stream.Close()

		// Track tool calls by index
		toolCalls := make(map[int64]*message.ToolCall)
		var toolCallOrder []int64

		for stream.Next() {
			event := stream.Current()
			switch ev := event.AsAny().(type) {
			case sdk.ContentBlockStartEvent:
				block := ev.ContentBlock
				switch b := block.AsAny().(type) {
				case sdk.ToolUseBlock:
					tc := &message.ToolCall{ID: b.ID, Name: b.Name}
					toolCalls[ev.Index] = tc
					toolCallOrder = append(toolCallOrder, ev.Index)
				case sdk.ServerToolUseBlock:
					slog.Debug("anthropic_server_tool_use", "name", string(b.Name), "id", b.ID)
				case sdk.WebSearchToolResultBlock:
					slog.Debug("anthropic_web_search_result", "tool_use_id", b.ToolUseID)
				}
			case sdk.ContentBlockDeltaEvent:
				delta := ev.Delta
				switch d := delta.AsAny().(type) {
				case sdk.TextDelta:
					out <- message.ChatResponse{Content: d.Text}
				case sdk.InputJSONDelta:
					if tc, ok := toolCalls[ev.Index]; ok {
						tc.Arguments = append(tc.Arguments, []byte(d.PartialJSON)...)
					}
				}
			}
		}
		if err := stream.Err(); err != nil {
			slog.Error("anthropic_stream_error", "error", err)
			out <- message.ChatResponse{Error: err}
			return
		}

		if len(toolCallOrder) > 0 {
			finalCalls := make([]message.ToolCall, 0, len(toolCallOrder))
			for _, idx := range toolCallOrder {
				tc := *toolCalls[idx]
				finalCalls = append(finalCalls, tc)
			}
			out <- message.ChatResponse{ToolCalls: finalCalls}
		}
		out <- message.ChatResponse{Done: true}
	}()
	return out, nil
}

func extractSystem(messages []message.Message) string {
	for _, m := range messages {
		if m.Role == message.RoleSystem {
			for _, p := range m.Parts {
				if tp, ok := p.(message.TextPart); ok {
					return tp.Text
				}
			}
		}
	}
	return ""
}

func toSDKMessages(messages []message.Message) []sdk.MessageParam {
	var out []sdk.MessageParam
	for _, m := range messages {
		if m.Role == message.RoleSystem {
			continue // handled separately
		}

		role := sdk.MessageParamRoleUser
		if m.Role == message.RoleAssistant {
			role = sdk.MessageParamRoleAssistant
		}

		var blocks []sdk.ContentBlockParamUnion
		for _, p := range m.Parts {
			switch v := p.(type) {
			case message.TextPart:
				blocks = append(blocks, sdk.ContentBlockParamUnion{
					OfText: &sdk.TextBlockParam{Text: v.Text},
				})
			case message.ToolCallPart:
				blocks = append(blocks, sdk.ContentBlockParamUnion{
					OfToolUse: &sdk.ToolUseBlockParam{
						ID:    v.ID,
						Name:  v.Name,
						Input: json.RawMessage(v.Arguments),
					},
				})
			case message.ToolResultPart:
				blocks = append(blocks, sdk.ContentBlockParamUnion{
					OfToolResult: &sdk.ToolResultBlockParam{
						ToolUseID: v.ToolCallID,
						Content: []sdk.ToolResultBlockParamContentUnion{{
							OfText: &sdk.TextBlockParam{Text: v.Content},
						}},
					},
				})
			}
		}

		if len(blocks) > 0 {
			// Merge consecutive same-role messages
			if len(out) > 0 && out[len(out)-1].Role == role {
				out[len(out)-1].Content = append(out[len(out)-1].Content, blocks...)
			} else {
				out = append(out, sdk.MessageParam{Role: role, Content: blocks})
			}
		}
	}
	return out
}

func toSDKTools(tools []message.ToolDefinition) []sdk.ToolUnionParam {
	var out []sdk.ToolUnionParam
	for _, t := range tools {
		switch {
		case t.Type == "web_search_20250305":
			// Promote to native Anthropic web search
			out = append(out, sdk.ToolUnionParam{
				OfWebSearchTool20250305: &sdk.WebSearchTool20250305Param{},
			})
		default:
			// Regular function tool
			var props map[string]any
			json.Unmarshal(t.Parameters, &props)

			schema := sdk.ToolInputSchemaParam{
				Type: "object",
			}
			if p, ok := props["properties"]; ok {
				schema.Properties = p
			}
			if r, ok := props["required"]; ok {
				if req, ok := r.([]any); ok {
					strs := make([]string, len(req))
					for j, s := range req {
						strs[j], _ = s.(string)
					}
					schema.Required = strs
				}
			}

			out = append(out, sdk.ToolUnionParam{
				OfTool: &sdk.ToolParam{
					Name:        t.Name,
					Description: sdk.String(t.Description),
					InputSchema: schema,
				},
			})
		}
	}
	return out
}

func listToolNames(tools []sdk.ToolUnionParam) []string {
	var names []string
	for _, t := range tools {
		if t.OfTool != nil {
			names = append(names, t.OfTool.Name)
		}
		if t.OfWebSearchTool20250305 != nil {
			names = append(names, "web_search_20250305")
		}
	}
	return names
}

func init() {
	remote.Register("anthropic", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		apiKey := options["api_key"]
		return &AnthropicProvider{APIKey: apiKey, BaseURL: options["base_url"]}, nil
	})
}
