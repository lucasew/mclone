package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lucasew/mclone/pkg/remote"
)

func (p *LocalProvider) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	path := filepath.Join(p.Path, name)
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

func (p *LocalProvider) Put(ctx context.Context, name string, size int64, data io.Reader) error {
	path := filepath.Join(p.Path, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, data)
	return err
}

func (p *LocalProvider) Chat(ctx context.Context, req remote.ChatRequest) (<-chan remote.ChatResponse, error) {
	return nil, fmt.Errorf("local provider does not support inference yet (requires local runner)")
}

func init() {
	remote.Register("local", func(name string, options map[string]string) (remote.Provider, error) {
		return &LocalProvider{Path: options["path"]}, nil
	})
}

type LocalProvider struct {
	Path string
}

func (p *LocalProvider) Name() string {
	return "local"
}

func (p *LocalProvider) List(ctx context.Context) ([]remote.Model, error) {
	entries, err := os.ReadDir(p.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return []remote.Model{}, nil
		}
		return nil, err
	}

	var models []remote.Model
	for _, entry := range entries {
		if !entry.IsDir() {
			info, _ := entry.Info()
			models = append(models, remote.Model{
				Name: entry.Name(),
				Size: info.Size(),
				ID:   filepath.Join(p.Path, entry.Name()),
			})
		}
	}
	return models, nil
}
