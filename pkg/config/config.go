package config

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lucasew/mclone/pkg/monitor"
	"github.com/pelletier/go-toml/v2"
)

type contextKey struct{}

// WithLoader stores a ConfigLoader in the context.
func WithLoader(ctx context.Context, loader *ConfigLoader) context.Context {
	return context.WithValue(ctx, contextKey{}, loader)
}

// LoaderFrom extracts the ConfigLoader from the context.
func LoaderFrom(ctx context.Context) *ConfigLoader {
	l, ok := ctx.Value(contextKey{}).(*ConfigLoader)
	if !ok {
		panic("config.LoaderFrom: no ConfigLoader in context")
	}
	return l
}

type RemoteConfig struct {
	Type          string         `toml:"type"`
	Export        bool           `toml:"export"`
	MaxConcurrent int            `toml:"max_concurrent"`
	Options       map[string]any `toml:"options"`
}

type ToolConfig struct {
	Type    string         `toml:"type"`
	Export  bool           `toml:"export"`
	Options map[string]any `toml:"options"`
}

type Config struct {
	Remotes map[string]RemoteConfig `toml:"remotes"`
	Tools   map[string]ToolConfig   `toml:"tools"`
}

type ConfigLoader struct {
	Location string
}

func (c *ConfigLoader) GetLocation() (string, error) {
	if c.Location == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		confPath := filepath.Join(home, ".config", "mclone", "mclone.conf")
		return confPath, err
	}
	_, err := os.Stat(c.Location)
	if err == nil {
		return c.Location, nil
	}
	if err == os.ErrNotExist {
		f, err := os.Create(c.Location)
		if err != nil {
			return "", err
		}
		defer func() {
			if err := f.Close(); err != nil {
				monitor.ReportError(context.Background(), err, "action", "config_file_close_error")
			}
		}()
	}
	return c.Location, nil
}

func (c *ConfigLoader) Load() (*Config, error) {
	configFile, err := c.GetLocation()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{
				Remotes: make(map[string]RemoteConfig),
				Tools:   make(map[string]ToolConfig),
			}, nil
		}
		return nil, err
	}

	var conf Config
	if err := toml.Unmarshal(data, &conf); err != nil {
		return nil, err
	}
	return &conf, nil
}

func (c *ConfigLoader) Save(cfg *Config) error {
	configFile, err := c.GetLocation()
	if err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, data, 0644)
}
