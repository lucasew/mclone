package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/tmc/langchaingo/llms/googleai"
)

type GeminiProvider struct {
	APIKey string
}

func (p *GeminiProvider) Name() string { return "gemini" }

func (p *GeminiProvider) List(ctx context.Context) ([]remote.Model, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", p.APIKey)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]remote.Model, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, remote.Model{Name: m.DisplayName, Slug: m.Name})
	}
	return models, nil
}


func (p *GeminiProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	llm, err := googleai.New(ctx, googleai.WithAPIKey(p.APIKey), googleai.WithDefaultModel(modelName))
	if err != nil {
		return nil, err
	}

	options.Tools = sanitizeTools(options.Tools)

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)

		var hasSentContent bool
		lcMsgs := message.ToLangChainMessages(messages)
		lcOpts := message.ToLangChainOptions(options, func(ctx context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				hasSentContent = true
				slog.Debug("gemini_chunk", "size", len(chunk))
				out <- message.ChatResponse{Content: string(chunk)}
			}
			return nil
		})

		slog.Debug("gemini_request", "model", modelName, "msgs_len", len(messages))
		resp, err := llm.GenerateContent(ctx, lcMsgs, lcOpts...)
		if err != nil {
			slog.Error("gemini_error", "error", fmt.Sprintf("%+v", err))
			out <- message.ChatResponse{Error: err}
			return
		}

		if len(resp.Choices) > 0 {
			aiMsg := resp.Choices[0]
			if !hasSentContent && aiMsg.Content != "" {
				slog.Debug("gemini_fallback_content", "len", len(aiMsg.Content))
				out <- message.ChatResponse{Content: aiMsg.Content}
			}
			if len(aiMsg.ToolCalls) > 0 {
				slog.Info("gemini_tool_calls_detected", "count", len(aiMsg.ToolCalls))
				out <- message.ChatResponse{ToolCalls: message.ToolCallsFromLangChain(aiMsg.ToolCalls)}
			}
		}
		out <- message.ChatResponse{Done: true}
	}()

	return out, nil
}

func sanitizeTools(tools []message.ToolDefinition) []message.ToolDefinition {
	out := make([]message.ToolDefinition, len(tools))
	for i, t := range tools {
		out[i] = message.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  sanitizeSchema(t.Parameters),
		}
	}
	return out
}

func sanitizeSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	clean := make(map[string]any, len(schema))
	for k, v := range schema {
		switch k {
		case "$schema", "additionalProperties", "exclusiveMinimum", "exclusiveMaximum":
			continue
		case "properties":
			if props, ok := v.(map[string]any); ok {
				cleanProps := make(map[string]any, len(props))
				for pk, pv := range props {
					if pm, ok := pv.(map[string]any); ok {
						cleanProps[pk] = sanitizeSchema(pm)
					} else {
						cleanProps[pk] = pv
					}
				}
				clean[k] = cleanProps
			} else {
				clean[k] = v
			}
		case "items":
			if items, ok := v.(map[string]any); ok {
				clean[k] = sanitizeSchema(items)
			} else {
				clean[k] = v
			}
		default:
			clean[k] = v
		}
	}
	return clean
}

func init() {
	remote.Register("gemini", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		return &GeminiProvider{APIKey: options["api_key"]}, nil
	})
}
