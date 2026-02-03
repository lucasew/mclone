package huggingface

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/lucasew/mclone/pkg/remote"
)

type HFProvider struct {
	Namespace string // user or org
}

func (p *HFProvider) Name() string {
	return "huggingface"
}

func (p *HFProvider) List(ctx context.Context) ([]remote.Model, error) {
	url := fmt.Sprintf("https://huggingface.co/api/models?author=%s", p.Namespace)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var results []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, err
	}

	var models []remote.Model
	for _, r := range results {
		models = append(models, remote.Model{
			Name: r.ID,
			ID:   r.ID,
		})
	}
	return models, nil
}

func (p *HFProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	// name format: user/repo/filename
	parts := strings.Split(name, "/")
	if len(parts) < 3 {
		return nil, 0, fmt.Errorf("invalid HF model path: %s (expected user/repo/filename)", name)
	}
	repo := strings.Join(parts[:2], "/")
	filename := strings.Join(parts[2:], "/")

	url := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", repo, filename)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("huggingface returned status %d", resp.StatusCode)
	}

	return resp.Body, resp.ContentLength, nil
}

func (p *HFProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	return fmt.Errorf("read-only provider")
}

func (p *HFProvider) Chat(ctx context.Context, req remote.ChatRequest) (<-chan remote.ChatResponse, error) {
	return nil, fmt.Errorf("huggingface provider does not support direct inference (use local or ollama)")
}

func init() {
	remote.Register("huggingface", func(name string, options map[string]string) (remote.Provider, error) {
		return &HFProvider{Namespace: options["namespace"]}, nil
	})
}
