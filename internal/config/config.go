package config

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Monitor   MonitorConfig   `yaml:"monitor"`
	Notify    NotifyConfig    `yaml:"notify"`
	Providers ProvidersConfig `yaml:"providers"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
}

type MonitorConfig struct {
	Interval string `yaml:"interval"`
	DataFile string `yaml:"data_file"`
}

type NotifyConfig struct {
	AppriseURLs []string     `yaml:"apprise_urls"`
	Rules       []NotifyRule `yaml:"rules"`
}

type NotifyRule struct {
	Event     string   `yaml:"event"`     // threshold, depleted, probe_error, reset_soon, status_change
	Threshold float64  `yaml:"threshold"` // for event=threshold: notify when any quota below this %
	Before    string   `yaml:"before"`    // for event=reset_soon: duration before reset (e.g. "10m")
	Providers []string `yaml:"providers"` // optional: filter by provider names (empty = all)
}

type ProvidersConfig struct {
	Claude  ClaudeConfig  `yaml:"claude"`
	Copilot CopilotConfig `yaml:"copilot"`
	Cursor  CursorConfig  `yaml:"cursor"`
}

type ClaudeConfig struct {
	Enabled bool     `yaml:"enabled"`
	Paths   []string `yaml:"paths"` // 额外凭证路径（默认 ~/.claude/ 自动读取）
}

type CopilotConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

type CursorConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Addr: ":8080",
		},
		Monitor: MonitorConfig{
			Interval: "60s",
		},
		Notify: NotifyConfig{
			Rules: []NotifyRule{
				{Event: "depleted"},
				{Event: "probe_error"},
			},
		},
		Providers: ProvidersConfig{
			Claude:  ClaudeConfig{Enabled: true},
			Copilot: CopilotConfig{Enabled: true},
			Cursor:  CursorConfig{Enabled: true},
		},
	}
}

func (c *Config) Interval() (time.Duration, error) {
	return time.ParseDuration(c.Monitor.Interval)
}

func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		paths := []string{
			"config.yaml",
			"config.yml",
			"ai-usage.yaml",
			"ai-usage.yml",
			filepath.Join(os.Getenv("HOME"), ".config", "ai-usage", "config.yaml"),
			filepath.Join(os.Getenv("HOME"), ".ai-usage.yaml"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Reload reloads the config from the same file path
func (c *Config) Reload(path string) error {
	if path == "" {
		// Find config path using the same logic as Load
		paths := []string{
			"config.yaml",
			"config.yml",
			"ai-usage.yaml",
			"ai-usage.yml",
			filepath.Join(os.Getenv("HOME"), ".config", "ai-usage", "config.yaml"),
			filepath.Join(os.Getenv("HOME"), ".ai-usage.yaml"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}

	if path == "" {
		return nil // No config file, use defaults
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return yaml.Unmarshal(data, c)
}

// GetConfigPath returns the current config file path
func GetConfigPath() string {
	paths := []string{
		"config.yaml",
		"config.yml",
		"ai-usage.yaml",
		"ai-usage.yml",
		filepath.Join(os.Getenv("HOME"), ".config", "ai-usage", "config.yaml"),
		filepath.Join(os.Getenv("HOME"), ".ai-usage.yaml"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
