package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/lucasew/mclone/pkg/remote"
)

type GeminiProvider struct {
	APIKey string
}

type geminiChatRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (p *GeminiProvider) Name() string {
	return "gemini"
}

func (p *GeminiProvider) List(ctx context.Context) ([]remote.Model, error) {
	return []remote.Model{}, nil
}

func (p *GeminiProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("not supported")
}

func (p *GeminiProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("not supported")
}

func (p *GeminiProvider) Chat(ctx context.Context, req remote.ChatRequest) (<-chan remote.ChatResponse, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?key=%s", req.Model, p.APIKey)

	geminiReq := geminiChatRequest{}
	for _, m := range req.Messages {
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		geminiReq.Contents = append(geminiReq.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.Content}},
		})
	}

	body, _ := json.Marshal(geminiReq)
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

		dec := json.NewDecoder(resp.Body)
		// Gemini stream is a JSON array of objects
		if _, err := dec.Token(); err != nil {
			return
		}

		for dec.More() {
			var r geminiResponse
			if err := dec.Decode(&r); err != nil {
				out <- remote.ChatResponse{Error: err}
				return
			}
			if len(r.Candidates) > 0 && len(r.Candidates[0].Content.Parts) > 0 {
				out <- remote.ChatResponse{
					Content: r.Candidates[0].Content.Parts[0].Text,
				}
			}
		}
	}()

	return out, nil
}

func init() {
	remote.Register("gemini", func(name string, options map[string]string) (remote.Provider, error) {
		return &GeminiProvider{APIKey: options["api_key"]}, nil
	})
}
