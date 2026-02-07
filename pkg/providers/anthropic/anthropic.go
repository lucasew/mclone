package anthropic

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/tmc/langchaingo/llms/anthropic"
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
	llm, err := anthropic.New(anthropic.WithToken(p.APIKey), anthropic.WithModel(modelName))
	if err != nil {
		return nil, err
	}

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)

		lcMsgs := message.ToLangChainMessages(messages)
		lcOpts := message.ToLangChainOptions(options, func(ctx context.Context, chunk []byte) error {
			out <- message.ChatResponse{Content: string(chunk)}
			return nil
		})

		resp, err := llm.GenerateContent(ctx, lcMsgs, lcOpts...)
		if err != nil {
			out <- message.ChatResponse{Error: err}
		} else {
			if len(resp.Choices) > 0 && len(resp.Choices[0].ToolCalls) > 0 {
				out <- message.ChatResponse{ToolCalls: message.ToolCallsFromLangChain(resp.Choices[0].ToolCalls)}
			}
			out <- message.ChatResponse{Done: true}
		}
	}()
	return out, nil
}

func init() {
	remote.Register("anthropic", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		apiKey := options["api_key"]
		return &AnthropicProvider{APIKey: apiKey}, nil
	})
}
