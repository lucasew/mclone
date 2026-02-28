package config

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type RemoteConfig struct {
	Type    string         `toml:"type"`
	Options map[string]any `toml:"options"`
}

type Config struct {
	Remotes map[string]RemoteConfig `toml:"remotes"`
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
		defer f.Close()
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
