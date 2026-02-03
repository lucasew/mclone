package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/lucasew/mclone/pkg/remote"
)

type OpenAIProvider struct {
	BaseURL string
	APIKey  string
}

type openaiChatRequest struct {
	Model    string               `json:"model"`
	Messages []remote.ChatMessage `json:"messages"`
	Stream   bool                 `json:"stream"`
}

type openaiChatResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func (p *OpenAIProvider) Name() string {
	return "openai"
}

func (p *OpenAIProvider) List(ctx context.Context) ([]remote.Model, error) {
	// For simplicity, we don't list all OpenAI models as the list is huge.
	// We could implement it via /v1/models if needed.
	return []remote.Model{}, nil
}

func (p *OpenAIProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("getting files from openai not supported")
}

func (p *OpenAIProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("uploading models to openai not supported")
}

func (p *OpenAIProvider) Chat(ctx context.Context, req remote.ChatRequest) (<-chan remote.ChatResponse, error) {
	url := fmt.Sprintf("%s/chat/completions", strings.TrimSuffix(p.BaseURL, "/"))

	body, err := json.Marshal(openaiChatRequest{
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

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.APIKey))

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan remote.ChatResponse)
	go func() {
		defer resp.Body.Close()
		defer close(out)

		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			out <- remote.ChatResponse{Error: fmt.Errorf("openai returned status %d: %s", resp.StatusCode, string(data))}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			line = strings.TrimPrefix(line, "data: ")
			if line == "[DONE]" {
				return
			}

			var r openaiChatResponse
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				continue
			}
			if len(r.Choices) > 0 {
				content := r.Choices[0].Delta.Content
				done := r.Choices[0].FinishReason != nil
				out <- remote.ChatResponse{
					Content: content,
					Done:    done,
				}
				if done {
					return
				}
			}
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
