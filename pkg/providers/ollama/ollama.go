package ollama

import (
	"context"
	"fmt"
	json "github.com/goccy/go-json"
	"log/slog"
	"net/http"
	"time"

	"github.com/lucasew/mclone/pkg/message"
	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/lucasew/mclone/pkg/providers/openaisdk"
	"github.com/lucasew/mclone/pkg/remote"

	sdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

type OllamaProvider struct {
	BaseURL string
}

type OllamaConfig struct {
	BaseURL string `mapstructure:"base_url"`
}

func (p *OllamaProvider) Name() string { return "ollama" }

func (p *OllamaProvider) List(ctx context.Context) ([]remote.Model, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("%s/api/tags", p.BaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("ollama list request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama list: unexpected status %d", resp.StatusCode)
	}

	var tags struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("ollama list decode: %w", err)
	}

	var models []remote.Model
	for _, m := range tags.Models {
		models = append(models, remote.Model{Name: m.Name, Slug: m.Name})
	}
	return models, nil
}

func (p *OllamaProvider) Chat(ctx context.Context, req message.Request) (<-chan message.Event, error) {
	client := sdk.NewClient(
		option.WithBaseURL(p.BaseURL+"/v1"),
		option.WithAPIKey("ollama"),
	)

	params := sdk.ChatCompletionNewParams{
		Model:    req.Model, // string
		Messages: openaisdk.ToMessages(req.Turns),
	}

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
		params.Tools = openaisdk.ToTools(req.Options.Tools, "ollama_tool_params_error")
	}

	stream := client.Chat.Completions.NewStreaming(ctx, params)

	out := make(chan message.Event)
	go func() {
		defer close(out)
		defer stream.Close()

		startTime := time.Now()

		acc := &sdk.ChatCompletionAccumulator{}

		for stream.Next() {
			chunk := stream.Current()
			acc.AddChunk(chunk)

			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					out <- message.TextDelta{Text: choice.Delta.Content}
				}
			}

			if tc, ok := acc.JustFinishedToolCall(); ok {
				slog.Info("ollama_tool_call", "name", tc.Name)
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
			monitor.ReportError(ctx, err, "action", "ollama_stream_error")
			out <- message.ResponseError{Err: err}
			return
		}

		slog.Debug("ollama_request_done", "total_duration", time.Since(startTime).String())
		out <- message.ResponseCompleted{Reason: message.StopReasonEndTurn}
	}()
	return out, nil
}

func init() {
	remote.Register("ollama", func(name string, options map[string]any, _ remote.Resolver) (remote.Provider, error) {
		var cfg OllamaConfig
		if err := remote.DecodeOptions(options, &cfg); err != nil {
			return nil, err
		}
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return &OllamaProvider{BaseURL: baseURL}, nil
	})
}
