package gemini

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/remote"
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

func (p *GeminiProvider) Chat(ctx context.Context, modelName string, messages []message.Message, options message.ChatOptions) (<-chan message.ChatResponse, error) {
	llm, err := googleai.New(ctx, googleai.WithAPIKey(p.APIKey), googleai.WithDefaultModel(modelName))
	if err != nil {
		return nil, err
	}

	out := make(chan message.ChatResponse)
	go func() {
		defer close(out)

		var hasSentContent bool
		lcMsgs := message.ToLangChainMessages(messages)
		lcOpts := message.ToLangChainOptions(options, func(ctx context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				hasSentContent = true
				slog.Debug("gemini_chunk", "size", len(chunk))
				out <- message.ChatResponse{Content: string(chunk)}
			}
			return nil
		})

		slog.Debug("gemini_request", "model", modelName, "msgs_len", len(messages))
		resp, err := llm.GenerateContent(ctx, lcMsgs, lcOpts...)
		if err != nil {
			slog.Error("gemini_error", "error", err)
			out <- message.ChatResponse{Error: err}
			return
		}

		if len(resp.Choices) > 0 {
			aiMsg := resp.Choices[0]
			if !hasSentContent && aiMsg.Content != "" {
				slog.Debug("gemini_fallback_content", "len", len(aiMsg.Content))
				out <- message.ChatResponse{Content: aiMsg.Content}
			}
			if len(aiMsg.ToolCalls) > 0 {
				slog.Info("gemini_tool_calls_detected", "count", len(aiMsg.ToolCalls))
				out <- message.ChatResponse{ToolCalls: message.ToolCallsFromLangChain(aiMsg.ToolCalls)}
			}
		}
		out <- message.ChatResponse{Done: true}
	}()

	return out, nil
}

func init() {
	remote.Register("gemini", func(name string, options map[string]string) (remote.Provider, error) {
		return &GeminiProvider{APIKey: options["api_key"]}, nil
	})
}
