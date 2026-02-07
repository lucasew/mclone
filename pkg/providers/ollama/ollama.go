package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
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
		models = append(models, remote.Model{Name: m.Name, Slug: m.Name})
	}
	return models, nil
}

func (p *OllamaProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	llm, err := ollama.New(
		ollama.WithServerURL(p.BaseURL),
		ollama.WithModel(modelName),
	)
	if err != nil {
		return nil, err
	}

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)

		startTime := time.Now()
		var hasSentContent bool

		lcMsgs := message.ToLangChainMessages(messages)
		lcOpts := message.ToLangChainOptions(options, func(ctx context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				if !hasSentContent {
					slog.Debug("ollama_first_token", "latency", time.Since(startTime).String())
					hasSentContent = true
				}
				out <- message.ChatResponse{Content: string(chunk)}
			}
			return nil
		})

		slog.Debug("ollama_request_start", "model", modelName)
		resp, err := llm.GenerateContent(ctx, lcMsgs, lcOpts...)
		if err != nil {
			slog.Error("ollama_error", "error", err)
			out <- message.ChatResponse{Error: err}
			return
		}

		if len(resp.Choices) > 0 {
			aiMsg := resp.Choices[0]
			if !hasSentContent && aiMsg.Content != "" {
				slog.Debug("ollama_fallback_content", "len", len(aiMsg.Content))
				out <- message.ChatResponse{Content: aiMsg.Content}
			}
			if len(aiMsg.ToolCalls) > 0 {
				slog.Info("ollama_tool_calls", "count", len(aiMsg.ToolCalls))
				out <- message.ChatResponse{ToolCalls: message.ToolCallsFromLangChain(aiMsg.ToolCalls)}
			}
		}
		slog.Debug("ollama_request_done", "total_duration", time.Since(startTime).String())
		out <- message.ChatResponse{Done: true}
	}()
	return out, nil
}


func init() {
	remote.Register("ollama", func(name string, options map[string]string, _ remote.Resolver) (remote.Provider, error) {
		baseURL := options["base_url"]
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return &OllamaProvider{BaseURL: baseURL}, nil
	})
}
