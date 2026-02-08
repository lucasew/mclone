package ollama

import (
	"context"
	"fmt"
	json "github.com/goccy/go-json"
	"log/slog"
	"net/http"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

type OllamaProvider struct {
	BaseURL string
}

type OllamaConfig struct {
	BaseURL string `mapstructure:"base_url"`
}

func (p *OllamaProvider) Name() string { return "ollama" }

func (p *OllamaProvider) List(ctx context.Context) ([]remote.Model, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("%s/api/tags", p.BaseURL)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var tags struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}

	var models []remote.Model
	for _, m := range tags.Models {
		models = append(models, remote.Model{Name: m.Name, Slug: m.Name})
	}
	return models, nil
}

func (p *OllamaProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	client := sdk.NewClient(
		option.WithBaseURL(p.BaseURL+"/v1"),
		option.WithAPIKey("ollama"),
	)

	params := sdk.ChatCompletionNewParams{
		Model:    modelName, // string
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

		startTime := time.Now()

		acc := &sdk.ChatCompletionAccumulator{}

		for stream.Next() {
			chunk := stream.Current()
			acc.AddChunk(chunk)

			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					out <- message.ChatResponse{Content: choice.Delta.Content}
				}
			}

			if tc, ok := acc.JustFinishedToolCall(); ok {
				slog.Info("ollama_tool_call", "name", tc.Name)
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
			slog.Error("ollama_stream_error", "error", err)
			out <- message.ChatResponse{Error: err}
			return
		}

		slog.Debug("ollama_request_done", "total_duration", time.Since(startTime).String())
		out <- message.ChatResponse{Done: true}
	}()
	return out, nil
}

func toSDKMessages(messages []message.Message) []sdk.ChatCompletionMessageParamUnion {
	var out []sdk.ChatCompletionMessageParamUnion
	for _, m := range messages {
		switch m.Role {
		case message.RoleSystem:
			var text string
			for _, p := range m.Parts {
				if tp, ok := p.(message.TextPart); ok {
					text += tp.Text
				}
			}
			out = append(out, sdk.SystemMessage(text))
		case message.RoleUser:
			var text string
			for _, p := range m.Parts {
				switch v := p.(type) {
				case message.TextPart:
					text += v.Text
				case message.ToolResultPart:
					out = append(out, sdk.ToolMessage(v.ToolCallID, v.Content))
				}
			}
			if text != "" {
				out = append(out, sdk.UserMessage(text))
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
						ID: v.ID,
						Function: sdk.ChatCompletionMessageToolCallFunctionParam{
							Name:      v.Name,
							Arguments: string(v.Arguments),
						},
					})
				}
			}
			if len(toolCalls) > 0 {
				out = append(out, sdk.ChatCompletionMessageParamUnion{
					OfAssistant: &sdk.ChatCompletionAssistantMessageParam{
						Content: sdk.ChatCompletionAssistantMessageParamContentUnion{
							OfString: sdk.String(textContent),
						},
						ToolCalls: toolCalls,
					},
				})
			} else {
				out = append(out, sdk.AssistantMessage(textContent))
			}
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
			continue
		}
		var params shared.FunctionParameters
		json.Unmarshal(t.Parameters, &params)

		out = append(out, sdk.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,                    // string
				Description: sdk.String(t.Description), // param.Opt[string]
				Parameters:  params,                    // shared.FunctionParameters
			},
		})
	}
	return out
}

func init() {
	remote.Register("ollama", func(name string, options map[string]any, _ remote.Resolver) (remote.Provider, error) {
		var cfg OllamaConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return &OllamaProvider{BaseURL: baseURL}, nil
	})
}
