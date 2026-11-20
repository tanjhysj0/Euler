// Package config 加载并校验 Euler 的运行时配置。
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
	defaultPrompt       = "coding"
	defaultMaxRounds    = 12
	defaultMaxToolCalls = 40
	defaultMaxDuration  = 15 * time.Minute
	defaultMaxOutput    = 64 * 1024
	defaultShellTimeout = 2 * time.Minute
	defaultShellOutput  = 1024 * 1024
	defaultModelTimeout = 120 * time.Second
	defaultPreview      = 1024 * 1024
	minAgentCount       = 1
	minAgentDuration    = time.Nanosecond
	minAgentOutput      = 1024
	noDuration          = time.Duration(0)
	emptyValue          = ""
)

// validReasoningEffort 限制可透传的推理强度取值。
var validReasoningEffort = map[string]bool{
	"low": true, "medium": true, "high": true, "max": true,
}

// AgentLimits 限制单次 Run 的模型轮次、工具调用次数、时长和输出。
type AgentLimits struct {
	MaxModelRounds int           `yaml:"max_model_rounds"`
	MaxToolCalls   int           `yaml:"max_tool_calls"`
	MaxDuration    time.Duration `yaml:"max_duration"`
	MaxOutputBytes int           `yaml:"max_output_bytes"`
}

// Config 是完整的运行时配置。数据库 URL 只从环境变量读取，
// 绝不会被序列化。
type Config struct {
	Listen           string        `yaml:"listen"`
	WorkspaceRoot    string        `yaml:"workspace_root"`
	AllowedOrigins   []string      `yaml:"allowed_origins"`
	DatabaseURL      string        `yaml:"-"`
	Agent            AgentLimits   `yaml:"agent"`
	ShellTimeout     time.Duration `yaml:"shell_timeout"`
	ShellOutputBytes int           `yaml:"shell_output_bytes"`
	ShellPathPrefix  string        `yaml:"shell_path_prefix"`
	ModelTimeout     time.Duration `yaml:"model_timeout"`
	// ModelReasoningEffort 为空时使用模型默认强度；可选 low/medium/high/max。
	ModelReasoningEffort string `yaml:"model_reasoning_effort"`
	// SystemPromptTemplate 选择 internal/agent/prompts/<name>.md 模板；
	// 为空时使用默认的 coding 模板。
	SystemPromptTemplate string `yaml:"system_prompt_template"`
	PreviewBytes         int    `yaml:"preview_bytes"`
}

// Load 在内置默认值之上读取 path 指定的文件，从 EULER_DATABASE_URL
// 填充数据库 URL，并校验结果。文件缺失不算错误。
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
		ShellTimeout:         defaultShellTimeout,
		ShellOutputBytes:     defaultShellOutput,
		ModelTimeout:         defaultModelTimeout,
		SystemPromptTemplate: defaultPrompt,
		PreviewBytes:         defaultPreview,
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
	if cfg.DatabaseURL == emptyValue {
		return Config{}, errors.New("EULER_DATABASE_URL is required")
	}
	if err = cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if err := c.Agent.validate(); err != nil {
		return err
	}
	return c.validateRuntime()
}

func (l AgentLimits) validate() error {
	if l.MaxModelRounds < minAgentCount || l.MaxToolCalls < minAgentCount ||
		l.MaxDuration < minAgentDuration || l.MaxOutputBytes < minAgentOutput {
		return errors.New("invalid agent limits")
	}
	return nil
}

func (c Config) validateRuntime() error {
	if !strings.HasPrefix(c.Listen, "127.0.0.1:") &&
		!strings.HasPrefix(c.Listen, "localhost:") {
		return errors.New("listen must be loopback in phase one")
	}
	if c.ShellPathPrefix != emptyValue && !filepath.IsAbs(c.ShellPathPrefix) {
		return errors.New("shell_path_prefix must be absolute")
	}
	if c.ModelTimeout <= noDuration {
		return errors.New("model_timeout must be positive")
	}
	if c.ModelReasoningEffort != emptyValue && !validReasoningEffort[c.ModelReasoningEffort] {
		return errors.New("model_reasoning_effort must be low, medium, high or max")
	}
	if !validPromptName(c.SystemPromptTemplate) {
		return errors.New("system_prompt_template must match ^[a-z0-9_-]+$")
	}
	return nil
}

// validPromptName 限制模板名只含小写字母、数字、下划线和连字符，
// 避免拼接路径时越界读取其他文件。
func validPromptName(name string) bool {
	if name == emptyValue {
		return true
	}
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9':
		case char == '_' || char == '-':
		default:
			return false
		}
	}
	return true
}
