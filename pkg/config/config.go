package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"io/fs"
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
		panic(fmt.Sprintf("config.LoaderFrom: no ConfigLoader in context"))
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

// configFileMode is owner read/write only. Config files hold API keys, OAuth
// tokens, and similar secrets in remote options.
const configFileMode = 0o600

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
	// os.Stat returns *fs.PathError wrapping ErrNotExist; == never matches.
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Create with 0600 so a brand-new secret-bearing conf is never group/world readable.
	// os.Create uses 0666 before umask (typically 0644).
	f, err := os.OpenFile(c.Location, os.O_RDWR|os.O_CREATE|os.O_EXCL, configFileMode)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return c.Location, nil
}

// tightenConfigPerms sets the config path to owner-only when group/other bits are set.
// WriteFile does not change mode on truncate of an existing file.
func tightenConfigPerms(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return os.Chmod(path, configFileMode)
}

func (c *ConfigLoader) Load() (*Config, error) {
	configFile, err := c.GetLocation()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Config{
				Remotes: make(map[string]RemoteConfig),
				Tools:   make(map[string]ToolConfig),
			}, nil
		}
		return nil, err
	}

	// Retroactively lock down world/group-readable configs that already hold secrets.
	if err := tightenConfigPerms(configFile); err != nil {
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
	if err := os.WriteFile(configFile, data, configFileMode); err != nil {
		return err
	}
	// WriteFile only applies mode on create; always enforce owner-only after write.
	return tightenConfigPerms(configFile)
}
