// Package config loads and validates the Euler runtime configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultListenAddr   = "127.0.0.1:8080"
	defaultMaxRounds    = 12
	defaultMaxToolCalls = 40
	defaultMaxDuration  = 15 * time.Minute
	defaultMaxOutput    = 64 * 1024
	defaultShellTimeout = 2 * time.Minute
	defaultShellOutput  = 1024 * 1024
	defaultPreview      = 1024 * 1024
	minAgentCount       = 1
	minAgentDuration    = time.Nanosecond
	minAgentOutput      = 1024
)

// AgentLimits caps a single Run's model rounds, tool calls, duration and output.
type AgentLimits struct {
	MaxModelRounds int           `yaml:"max_model_rounds"`
	MaxToolCalls   int           `yaml:"max_tool_calls"`
	MaxDuration    time.Duration `yaml:"max_duration"`
	MaxOutputBytes int           `yaml:"max_output_bytes"`
}

// Config is the complete runtime configuration. The database URL is read from
// the environment only and is never serialized.
type Config struct {
	Listen           string        `yaml:"listen"`
	WorkspaceRoot    string        `yaml:"workspace_root"`
	AllowedOrigins   []string      `yaml:"allowed_origins"`
	DatabaseURL      string        `yaml:"-"`
	Agent            AgentLimits   `yaml:"agent"`
	ShellTimeout     time.Duration `yaml:"shell_timeout"`
	ShellOutputBytes int           `yaml:"shell_output_bytes"`
	PreviewBytes     int           `yaml:"preview_bytes"`
}

// Load reads path over the built-in defaults, fills the database URL from
// EULER_DATABASE_URL, and validates the result. A missing file is not an error.
func Load(path string) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("home: %w", err)
	}
	cfg := Config{
		Listen:        defaultListenAddr,
		WorkspaceRoot: filepath.Join(home, "EulerWorkspaces"),
		Agent: AgentLimits{
			MaxModelRounds: defaultMaxRounds,
			MaxToolCalls:   defaultMaxToolCalls,
			MaxDuration:    defaultMaxDuration,
			MaxOutputBytes: defaultMaxOutput,
		},
		ShellTimeout:     defaultShellTimeout,
		ShellOutputBytes: defaultShellOutput,
		PreviewBytes:     defaultPreview,
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	if err == nil {
		if err = yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, err
		}
	}
	cfg.DatabaseURL = os.Getenv("EULER_DATABASE_URL")
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("EULER_DATABASE_URL is required")
	}
	if err = cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.Agent.MaxModelRounds < minAgentCount || c.Agent.MaxToolCalls < minAgentCount ||
		c.Agent.MaxDuration < minAgentDuration || c.Agent.MaxOutputBytes < minAgentOutput {
		return errors.New("invalid agent limits")
	}
	if !strings.HasPrefix(c.Listen, "127.0.0.1:") &&
		!strings.HasPrefix(c.Listen, "localhost:") {
		return errors.New("listen must be loopback in phase one")
	}
	return nil
}
