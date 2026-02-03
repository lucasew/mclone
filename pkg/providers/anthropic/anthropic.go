package anthropic

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

type AnthropicProvider struct {
	BaseURL string
	APIKey  string
}

type anthropicChatRequest struct {
	Model     string               `json:"model"`
	Messages  []remote.ChatMessage `json:"messages"`
	MaxTokens int                  `json:"max_tokens"`
	Stream    bool                 `json:"stream"`
}

type anthropicEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
}

func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

func (p *AnthropicProvider) List(ctx context.Context) ([]remote.Model, error) {
	return []remote.Model{}, nil
}

func (p *AnthropicProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("getting files from anthropic not supported")
}

func (p *AnthropicProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("uploading models to anthropic not supported")
}

func (p *AnthropicProvider) Chat(ctx context.Context, req remote.ChatRequest) (<-chan remote.ChatResponse, error) {
	url := fmt.Sprintf("%s/messages", strings.TrimSuffix(p.BaseURL, "/"))

	body, err := json.Marshal(anthropicChatRequest{
		Model:     req.Model,
		Messages:  req.Messages,
		MaxTokens: 4096,
		Stream:    true,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

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
			out <- remote.ChatResponse{Error: fmt.Errorf("anthropic returned status %d: %s", resp.StatusCode, string(data))}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			line = strings.TrimPrefix(line, "data: ")

			var ev anthropicEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}

			if ev.Type == "content_block_delta" {
				out <- remote.ChatResponse{
					Content: ev.Delta.Text,
				}
			} else if ev.Type == "message_stop" {
				out <- remote.ChatResponse{Done: true}
				return
			}
		}
	}()

	return out, nil
}

func init() {
	remote.Register("anthropic", func(name string, options map[string]string) (remote.Provider, error) {
		baseURL := options["base_url"]
		if baseURL == "" {
			baseURL = "https://api.anthropic.com/v1"
		}
		apiKey := options["api_key"]
		return &AnthropicProvider{BaseURL: baseURL, APIKey: apiKey}, nil
	})
}
