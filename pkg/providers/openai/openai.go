package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
	"github.com/tmc/langchaingo/llms/openai"
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
	llm, err := openai.New(openai.WithBaseURL(p.BaseURL), openai.WithToken(p.APIKey), openai.WithModel(modelName))
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
	remote.Register("openai", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		baseURL := options["base_url"]
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		apiKey := options["api_key"]
		return &OpenAIProvider{BaseURL: baseURL, APIKey: apiKey}, nil
	})
}
