package openai

import (
	"context"
	json "github.com/goccy/go-json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

type OpenAIProvider struct {
	BaseURL string
	APIKey  string
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) List(ctx context.Context) ([]remote.Model, error) {
	url := fmt.Sprintf("%s/models", p.BaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
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

func (p *OpenAIProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	client := sdk.NewClient(
		option.WithBaseURL(p.BaseURL),
		option.WithAPIKey(p.APIKey),
	)

	params := sdk.ChatCompletionNewParams{
		Model:    sdk.ChatModel(modelName),
		Messages: toSDKMessages(messages),
	}

	if options.Temperature != nil {
		params.Temperature = sdk.Float(*options.Temperature)
	}
	if options.TopP != nil {
		params.TopP = sdk.Float(*options.TopP)
	}
	if options.MaxTokens != nil {
		params.MaxTokens = sdk.Int(int64(*options.MaxTokens))
	}
	if len(options.Stop) > 0 {
		if len(options.Stop) == 1 {
			params.Stop = sdk.ChatCompletionNewParamsStopUnion{
				OfString: sdk.String(options.Stop[0]),
			}
		} else {
			params.Stop = sdk.ChatCompletionNewParamsStopUnion{
				OfStringArray: options.Stop,
			}
		}
	}

	if len(options.Tools) > 0 {
		params.Tools = toSDKTools(options.Tools)
	}

	stream := client.Chat.Completions.NewStreaming(ctx, params)

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)
		defer stream.Close()

		acc := &sdk.ChatCompletionAccumulator{}

		for stream.Next() {
			chunk := stream.Current()
			acc.AddChunk(chunk)

			// Stream text content
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					out <- message.ChatResponse{Content: choice.Delta.Content}
				}
			}

			// Check for completed tool calls
			if tc, ok := acc.JustFinishedToolCall(); ok {
				out <- message.ChatResponse{
					ToolCalls: []message.ToolCall{{
						ID:        tc.ID,
						Name:      tc.Name,
						Arguments: json.RawMessage(tc.Arguments),
					}},
				}
			}
		}
		if err := stream.Err(); err != nil {
			slog.Error("openai_stream_error", "error", err)
			out <- message.ChatResponse{Error: err}
			return
		}

		out <- message.ChatResponse{Done: true}
	}()
	return out, nil
}

func toSDKMessages(messages []message.Message) []sdk.ChatCompletionMessageParamUnion {
	var out []sdk.ChatCompletionMessageParamUnion
	for _, m := range messages {
		switch m.Role {
		case message.RoleSystem:
			for _, p := range m.Parts {
				if tp, ok := p.(message.TextPart); ok {
					out = append(out, sdk.SystemMessage(tp.Text))
				}
			}
		case message.RoleUser:
			for _, p := range m.Parts {
				switch v := p.(type) {
				case message.TextPart:
					out = append(out, sdk.UserMessage(v.Text))
				case message.ToolResultPart:
					out = append(out, sdk.ToolMessage(v.ToolCallID, v.Content))
				}
			}
		case message.RoleAssistant:
			var textContent string
			var toolCalls []sdk.ChatCompletionMessageToolCallParam
			for _, p := range m.Parts {
				switch v := p.(type) {
				case message.TextPart:
					textContent += v.Text
				case message.ToolCallPart:
					toolCalls = append(toolCalls, sdk.ChatCompletionMessageToolCallParam{
						ID:   v.ID,
						Type: "function",
						Function: sdk.ChatCompletionMessageToolCallFunctionParam{
							Name:      v.Name,
							Arguments: string(v.Arguments),
						},
					})
				}
			}
			msg := sdk.ChatCompletionMessageParamUnion{
				OfAssistant: &sdk.ChatCompletionAssistantMessageParam{
					Content: sdk.ChatCompletionAssistantMessageParamContentUnion{
						OfString: sdk.String(textContent),
					},
				},
			}
			if len(toolCalls) > 0 {
				msg.OfAssistant.ToolCalls = toolCalls
			}
			out = append(out, msg)
		case message.RoleTool:
			for _, p := range m.Parts {
				if v, ok := p.(message.ToolResultPart); ok {
					out = append(out, sdk.ToolMessage(v.ToolCallID, v.Content))
				}
			}
		}
	}
	return out
}

func toSDKTools(tools []message.ToolDefinition) []sdk.ChatCompletionToolParam {
	var out []sdk.ChatCompletionToolParam
	for _, t := range tools {
		if t.Type != "" && t.Type != "function" {
			slog.Debug("openai_skip_tool", "name", t.Name, "type", t.Type)
			continue
		}
		var params shared.FunctionParameters
		json.Unmarshal(t.Parameters, &params)

		out = append(out, sdk.ChatCompletionToolParam{
			Type: "function",
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: sdk.String(t.Description),
				Parameters:  params,
			},
		})
	}
	return out
}

func init() {
	remote.Register("openai", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		baseURL := options["base_url"]
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		apiKey := options["api_key"]
		return &OpenAIProvider{BaseURL: baseURL, APIKey: apiKey}, nil
	})
}
