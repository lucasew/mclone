package gemini

import (
	"context"
	"fmt"
	"io"

	"github.com/lucasew/mclone/pkg/remote"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
)

type GeminiProvider struct {
	APIKey string
}

func (p *GeminiProvider) Name() string { return "gemini" }

func (p *GeminiProvider) List(ctx context.Context) ([]remote.Model, error) {
	return []remote.Model{}, nil
}

func (p *GeminiProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("not supported")
}

func (p *GeminiProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("not supported")
}

func (p *GeminiProvider) Chat(ctx context.Context, modelName string, messages []llms.MessageContent) (<-chan remote.ChatResponse, error) {
	llm, err := googleai.New(ctx, googleai.WithAPIKey(p.APIKey), googleai.WithDefaultModel(modelName))
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
	remote.Register("gemini", func(name string, options map[string]string) (remote.Provider, error) {
		return &GeminiProvider{APIKey: options["api_key"]}, nil
	})
}
