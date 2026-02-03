package config

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type RemoteConfig struct {
	Type    string            `toml:"type"`
	Options map[string]string `toml:"options"`
}

type Config struct {
	Remotes map[string]RemoteConfig `toml:"remotes"`
}

func LoadConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	confPath := filepath.Join(home, ".config", "mclone", "mclone.conf")

	data, err := os.ReadFile(confPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Remotes: make(map[string]RemoteConfig)}, nil
		}
		return nil, err
	}

	var conf Config
	if err := toml.Unmarshal(data, &conf); err != nil {
		return nil, err
	}
	return &conf, nil
}

func (c *Config) Save() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	confDir := filepath.Join(home, ".config", "mclone")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return err
	}

	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(confDir, "mclone.conf"), data, 0644)
}
