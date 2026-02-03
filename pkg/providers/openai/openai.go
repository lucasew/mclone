package openai

import (
	"context"
	"fmt"
	"io"

	"github.com/lucasew/mclone/pkg/remote"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

type OpenAIProvider struct {
	BaseURL string
	APIKey  string
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) List(ctx context.Context) ([]remote.Model, error) {
	return []remote.Model{}, nil
}

func (p *OpenAIProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("not supported")
}

func (p *OpenAIProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("not supported")
}

func (p *OpenAIProvider) Chat(ctx context.Context, modelName string, messages []llms.MessageContent) (<-chan remote.ChatResponse, error) {
	llm, err := openai.New(openai.WithBaseURL(p.BaseURL), openai.WithToken(p.APIKey), openai.WithModel(modelName))
	if err != nil {
		return nil, err
	}

	out := make(chan remote.ChatResponse)
	go func() {
		defer close(out)
		_, err := llm.GenerateContent(ctx, messages, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			out <- remote.ChatResponse{Content: string(chunk)}
			return nil
		}))
		if err != nil {
			out <- remote.ChatResponse{Error: err}
		} else {
			out <- remote.ChatResponse{Done: true}
		}
	}()
	return out, nil
}

func init() {
	remote.Register("openai", func(name string, options map[string]string) (remote.Provider, error) {
		baseURL := options["base_url"]
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		apiKey := options["api_key"]
		return &OpenAIProvider{BaseURL: baseURL, APIKey: apiKey}, nil
	})
}
