package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lucasew/mclone/pkg/remote"
)

type OllamaProvider struct {
	BaseURL string
}

type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"models"`
}

func (p *OllamaProvider) Name() string {
	return "ollama"
}

func (p *OllamaProvider) List(ctx context.Context) ([]remote.Model, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("%s/api/tags", p.BaseURL)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}

	var models []remote.Model
	for _, m := range tags.Models {
		models = append(models, remote.Model{
			Name: m.Name,
			Size: m.Size,
			ID:   m.Name,
		})
	}
	return models, nil
}

type ollamaChatRequest struct {
	Model    string               `json:"model"`
	Messages []remote.ChatMessage `json:"messages"`
	Stream   bool                 `json:"stream"`
}

type ollamaChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

func (p *OllamaProvider) Chat(ctx context.Context, req remote.ChatRequest) (<-chan remote.ChatResponse, error) {
	url := fmt.Sprintf("%s/api/chat", p.BaseURL)

	body, err := json.Marshal(ollamaChatRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   true,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan remote.ChatResponse)
	go func() {
		defer resp.Body.Close()
		defer close(out)

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			var r ollamaChatResponse
			if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
				out <- remote.ChatResponse{Error: err}
				return
			}
			out <- remote.ChatResponse{
				Content: r.Message.Content,
				Done:    r.Done,
			}
			if r.Done {
				return
			}
		}
	}()

	return out, nil
}

func (p *OllamaProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("pulling models from ollama not implemented yet")
}

func (p *OllamaProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("pushing models to ollama not implemented yet")
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
