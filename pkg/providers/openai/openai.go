package openai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	json "github.com/goccy/go-json"

	"github.com/lucasew/mclone/pkg/httpclient"
	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/providers/openaisdk"
	"github.com/lucasew/mclone/pkg/remote"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// Shared clients (pkg/httpclient): List for models, Stream for Chat SSE.
var listHTTPClient = httpclient.List
var streamHTTPClient = httpclient.Stream

// ErrListStatus is returned when the models endpoint responds non-2xx.
var ErrListStatus = errors.New("openai list models: unexpected status")

type OpenAIProvider struct {
	BaseURL string
	APIKey  string
}

type OpenAIConfig struct {
	APIKey  string `mapstructure:"api_key"`
	BaseURL string `mapstructure:"base_url"`
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) List(ctx context.Context) ([]remote.Model, error) {
	url := fmt.Sprintf("%s/models", p.BaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := listHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := httpclient.StatusError(resp, ErrListStatus, "openai list models"); err != nil {
		return nil, err
	}

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
		model := remote.Model{Name: m.ID, Slug: m.ID}
		if m.OwnedBy != "" {
			model.OwnedBy = []string{m.OwnedBy}
		}
		models = append(models, model)
	}
	return models, nil
}

func (p *OpenAIProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	client := sdk.NewClient(
		option.WithBaseURL(p.BaseURL),
		option.WithAPIKey(p.APIKey),
		option.WithHTTPClient(streamHTTPClient),
	)

	logOpenAIRequestDebug(ctx, req)

	params := sdk.ChatCompletionNewParams{
		Model:    req.Model,
		Messages: openaisdk.ToMessages(req.Turns),
	}

	logOpenAIParamsDebug(ctx, params)

	if req.Options.Temperature != nil {
		params.Temperature = sdk.Float(*req.Options.Temperature)
	}
	if req.Options.TopP != nil {
		params.TopP = sdk.Float(*req.Options.TopP)
	}
	if req.Options.MaxTokens != nil {
		params.MaxTokens = sdk.Int(int64(*req.Options.MaxTokens))
	}
	if len(req.Options.Stop) > 0 {
		if len(req.Options.Stop) == 1 {
			params.Stop = sdk.ChatCompletionNewParamsStopUnion{
				OfString: sdk.String(req.Options.Stop[0]),
			}
		} else {
			params.Stop = sdk.ChatCompletionNewParamsStopUnion{
				OfStringArray: req.Options.Stop,
			}
		}
	}

	if len(req.Options.Tools) > 0 {
		params.Tools = openaisdk.ToTools(ctx, req.Options.Tools, "openai_tool_params_error")
	}

	stream := client.Chat.Completions.NewStreaming(ctx, params)

	out := make(chan message.Event)
	go func() {
		defer close(out)
		defer stream.Close()

		acc := &sdk.ChatCompletionAccumulator{}
		var sawContent bool
		var sawToolCall bool

		for stream.Next() {
			chunk := stream.Current()
			acc.AddChunk(chunk)

			// Stream text content
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					sawContent = true
					out <- message.TextDelta{Text: choice.Delta.Content}
				}
			}

			// Check for completed tool calls
			if tc, ok := acc.JustFinishedToolCall(); ok {
				sawToolCall = true
				out <- message.ToolCallFinished{
					Call: message.ToolCall{
						ID:        tc.ID,
						Name:      tc.Name,
						Arguments: json.RawMessage(tc.Arguments),
					},
				}
			}
		}
		if err := stream.Err(); err != nil {
			if shouldIgnoreStreamError(err, sawContent, sawToolCall) {
				reason := message.StopReasonEndTurn
				if sawToolCall {
					reason = message.StopReasonToolCall
				}
				out <- message.ResponseCompleted{Reason: reason}
				return
			}
			monitor.ReportError(ctx, err, "action", "openai_stream_error")
			out <- message.ResponseError{Err: err}
			return
		}

		reason := message.StopReasonEndTurn
		if sawToolCall {
			reason = message.StopReasonToolCall
		}
		out <- message.ResponseCompleted{Reason: reason}
	}()
	return out, nil
}

func logOpenAIRequestDebug(ctx context.Context, req message.Request) {
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return
	}
	payload, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		slog.Debug("openai_request_debug_marshal_failed", "error", err)
		return
	}
	slog.Debug("openai_request_debug", "payload", string(payload))
}

func logOpenAIParamsDebug(ctx context.Context, params sdk.ChatCompletionNewParams) {
	if !slog.Default().Enabled(ctx, slog.LevelDebug) {
		return
	}
	payload, err := json.MarshalIndent(params, "", "  ")
	if err != nil {
		slog.Debug("openai_params_debug_marshal_failed", "error", err)
		return
	}
	slog.Debug("openai_params_debug", "payload", string(payload))
}

func shouldIgnoreStreamError(err error, sawContent, sawToolCall bool) bool {
	if err == nil || (!sawContent && !sawToolCall) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "NO_ERROR; received from peer") || strings.Contains(msg, "context canceled")
}

func init() {
	remote.Register("openai", func(name string, options map[string]any, _ remote.Resolver) (remote.Provider, error) {
		var cfg OpenAIConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		return &OpenAIProvider{BaseURL: baseURL, APIKey: cfg.APIKey}, nil
	})
}
