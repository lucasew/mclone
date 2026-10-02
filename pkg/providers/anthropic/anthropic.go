package anthropic

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	json "github.com/goccy/go-json"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/remote"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// listHTTPClient bounds List so a hung /v1/models endpoint cannot block forever.
// http.DefaultClient has no Timeout.
var listHTTPClient = &http.Client{Timeout: 30 * time.Second}

// streamHTTPClient is used for Chat SSE. No overall Timeout so long streams can
// complete; dial and response-header deadlines still bound connection stalls.
// The request context cancels the body read when the caller aborts.
var streamHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

type AnthropicProvider struct {
	BaseURL string
	APIKey  string
}

type AnthropicConfig struct {
	APIKey  string `mapstructure:"api_key"`
	BaseURL string `mapstructure:"base_url"`
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) List(ctx context.Context) ([]remote.Model, error) {
	base := "https://api.anthropic.com"
	if p.BaseURL != "" {
		base = strings.TrimRight(p.BaseURL, "/")
	}
	url := base + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := listHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
		if err != nil {
			return nil, fmt.Errorf("anthropic list models: status %d: read body: %w", resp.StatusCode, err)
		}
		msg := strings.TrimSpace(string(body))
		if msg != "" {
			return nil, fmt.Errorf("anthropic list models: status %d: %s", resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("anthropic list models: status %d", resp.StatusCode)
	}

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

func (p *AnthropicProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	opts := []option.RequestOption{
		option.WithAPIKey(p.APIKey),
		option.WithHTTPClient(streamHTTPClient),
	}
	if p.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(p.BaseURL))
	}
	client := sdk.NewClient(opts...)

	params := sdk.MessageNewParams{
		Model:    sdk.Model(req.Model),
		Messages: toSDKMessages(req.Turns),
	}

	// System prompt
	if sys := extractSystem(req.Turns); sys != "" {
		params.System = []sdk.TextBlockParam{{Text: sys}}
	}

	// Generation params
	if req.Options.MaxTokens != nil {
		params.MaxTokens = int64(*req.Options.MaxTokens)
	} else {
		params.MaxTokens = 8192
	}
	if req.Options.Temperature != nil {
		params.Temperature = sdk.Float(*req.Options.Temperature)
	}
	if req.Options.TopP != nil {
		params.TopP = sdk.Float(*req.Options.TopP)
	}
	if len(req.Options.Stop) > 0 {
		params.StopSequences = req.Options.Stop
	}

	// Tools
	if len(req.Options.Tools) > 0 {
		params.Tools = toSDKTools(req.Options.Tools)
		slog.Info("anthropic_tools_sent", "count", len(params.Tools), "names", listToolNames(params.Tools))
	}

	stream := client.Messages.NewStreaming(ctx, params)

	out := make(chan message.Event)
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
					out <- message.TextDelta{Text: d.Text}
				case sdk.InputJSONDelta:
					if tc, ok := toolCalls[ev.Index]; ok {
						tc.Arguments = append(tc.Arguments, []byte(d.PartialJSON)...)
						out <- message.ToolCallDelta{
							ID:             tc.ID,
							Name:           tc.Name,
							ArgumentsDelta: d.PartialJSON,
						}
					}
				}
			}
		}
		if err := stream.Err(); err != nil {
			monitor.ReportError(ctx, err, "action", "anthropic_stream_error")
			out <- message.ResponseError{Err: err}
			return
		}

		if len(toolCallOrder) > 0 {
			for _, idx := range toolCallOrder {
				tc := *toolCalls[idx]
				out <- message.ToolCallFinished{Call: tc}
			}
			out <- message.ResponseCompleted{Reason: message.StopReasonToolCall}
			return
		}
		out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
	}()
	return out, nil
}

func extractSystem(messages []message.Turn) string {
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

func toSDKMessages(messages []message.Turn) []sdk.MessageParam {
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
		switch t.Type {
		case "web_search_20250305":
			// Promote to native Anthropic web search
			out = append(out, sdk.ToolUnionParam{
				OfWebSearchTool20250305: &sdk.WebSearchTool20250305Param{},
			})
		default:
			// Regular function tool
			var props map[string]any
			if err := json.Unmarshal(t.Parameters, &props); err != nil {
				monitor.ReportError(context.Background(), err, "action", "anthropic_tool_params_error", "name", t.Name)
			}

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
	remote.Register("anthropic", func(name string, options map[string]any, _ remote.Resolver) (remote.Provider, error) {
		var cfg AnthropicConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		return &AnthropicProvider{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}, nil
	})
}
