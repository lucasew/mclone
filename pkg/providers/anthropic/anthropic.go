package anthropic

import (
	"context"
	"fmt"
	"io"

	"github.com/lucasew/mclone/pkg/remote"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
)

type AnthropicProvider struct {
	BaseURL string
	APIKey  string
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

func (p *AnthropicProvider) List(ctx context.Context) ([]remote.Model, error) {
	return []remote.Model{}, nil
}

func (p *AnthropicProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("not supported")
}

func (p *AnthropicProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("not supported")
}

func (p *AnthropicProvider) Chat(ctx context.Context, modelName string, messages []llms.MessageContent, options ...llms.CallOption) (<-chan remote.ChatResponse, error) {
	llm, err := anthropic.New(anthropic.WithToken(p.APIKey), anthropic.WithModel(modelName))
	if err != nil {
		return nil, err
	}

	out := make(chan remote.ChatResponse)
	go func() {
		defer close(out)

		finalOptions := append(options, llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			out <- remote.ChatResponse{Content: string(chunk)}
			return nil
		}))

		resp, err := llm.GenerateContent(ctx, messages, finalOptions...)
		if err != nil {
			out <- remote.ChatResponse{Error: err}
		} else {
			if len(resp.Choices) > 0 {
				aiMsg := resp.Choices[0]
				if len(aiMsg.ToolCalls) > 0 {
					out <- remote.ChatResponse{ToolCalls: aiMsg.ToolCalls}
				}
			}
			out <- remote.ChatResponse{Done: true}
		}
	}()
	return out, nil
}

func init() {
	remote.Register("anthropic", func(name string, options map[string]string) (remote.Provider, error) {
		apiKey := options["api_key"]
		return &AnthropicProvider{APIKey: apiKey}, nil
	})
}
