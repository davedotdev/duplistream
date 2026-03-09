package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig            `yaml:"server"`
	Outputs map[string]OutputConfig `yaml:"outputs"`
}

type ServerConfig struct {
	Listen     string `yaml:"listen"`
	App        string `yaml:"app"`
	StreamKey  string `yaml:"stream_key"`
	StatusPort string `yaml:"status_port"`
}

type OutputConfig struct {
	Enabled   bool   `yaml:"enabled"`
	URL       string `yaml:"url"`
	Key       string `yaml:"key"`
	AudioOnly bool   `yaml:"audio_only"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Expand environment variables in keys
	for name, output := range cfg.Outputs {
		output.Key = expandEnv(output.Key)
		cfg.Outputs[name] = output
	}

	// Set defaults
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = ":1935"
	}
	if cfg.Server.App == "" {
		cfg.Server.App = "live"
	}
	if cfg.Server.StatusPort == "" {
		cfg.Server.StatusPort = ":8080"
	}

	return &cfg, nil
}

// expandEnv expands ${VAR} or $VAR in strings
func expandEnv(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		envVar := s[2 : len(s)-1]
		return os.Getenv(envVar)
	}
	if strings.HasPrefix(s, "$") {
		return os.Getenv(s[1:])
	}
	return s
}
