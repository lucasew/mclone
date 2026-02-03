package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lucasew/mclone/pkg/remote"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
)

type OllamaProvider struct {
	BaseURL string
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
		models = append(models, remote.Model{Name: m.Name, Size: m.Size, ID: m.Name})
	}
	return models, nil
}

func (p *OllamaProvider) Chat(ctx context.Context, modelName string, messages []llms.MessageContent) (<-chan remote.ChatResponse, error) {
	llm, err := ollama.New(ollama.WithServerURL(p.BaseURL), ollama.WithModel(modelName))
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

func (p *OllamaProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("not implemented")
}

func (p *OllamaProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("not implemented")
}

func init() {
	remote.Register("ollama", func(name string, options map[string]string) (remote.Provider, error) {
		baseURL := options["base_url"]
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return &OllamaProvider{BaseURL: baseURL}, nil
	})
}
