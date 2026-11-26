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
	defaultListenAddr = "127.0.0.1:8080"
	defaultPrompt     = "coding"
	// 模型轮次与工具调用默认不设限，只保留单次 Run 的时长约束。
	defaultMaxRounds    = 0
	defaultMaxToolCalls = 0
	defaultMaxDuration  = 10 * time.Minute
	defaultMaxOutput    = 64 * 1024
	defaultShellTimeout = 2 * time.Minute
	defaultShellOutput  = 1024 * 1024
	// defaultModelTimeout 是单次模型请求的空闲超时。
	defaultModelTimeout = 10 * time.Second
	// defaultModelRequestTimeout 是单次模型请求的总时长上限。
	defaultModelRequestTimeout = 5 * time.Minute
	defaultPreview             = 1024 * 1024
	defaultLangfuseEnv         = "local"
	minAgentDuration           = time.Nanosecond
	minAgentOutput             = 1024
	noAgentLimit               = 0
	noDuration                 = time.Duration(0)
	emptyValue                 = ""
)

// validReasoningEffort 限制可透传的推理强度取值。
var validReasoningEffort = map[string]bool{
	"low": true, "medium": true, "high": true, "max": true,
}

// AgentLimits 限制单次 Run 的模型轮次、工具调用次数、时长和输出。
// MaxModelRounds 与 MaxToolCalls 为 0 表示不设限，只受 MaxDuration 约束。
type AgentLimits struct {
	MaxModelRounds int           `yaml:"max_model_rounds"`
	MaxToolCalls   int           `yaml:"max_tool_calls"`
	MaxDuration    time.Duration `yaml:"max_duration"`
	MaxOutputBytes int           `yaml:"max_output_bytes"`
}

// LangfuseConfig 配置 Langfuse 链路追踪。三项参数都可以写进配置文件，
// 也可以完全由环境变量提供；密钥不会随配置序列化落盘。
type LangfuseConfig struct {
	Host        string `yaml:"host"`
	PublicKey   string `yaml:"public_key"`
	SecretKey   string `yaml:"-"`
	Environment string `yaml:"environment"`
}

// Enabled 报告是否配置了 Langfuse。
func (l LangfuseConfig) Enabled() bool { return l.Host != emptyValue }

// Config 是完整的运行时配置。数据库 URL 与 Langfuse 密钥只从环境变量读取，
// 绝不会被序列化。
type Config struct {
	Listen              string         `yaml:"listen"`
	WorkspaceRoot       string         `yaml:"workspace_root"`
	AllowedOrigins      []string       `yaml:"allowed_origins"`
	DatabaseURL         string         `yaml:"-"`
	DatabaseDriver      string         `yaml:"-"`
	Langfuse            LangfuseConfig `yaml:"langfuse"`
	Agent               AgentLimits    `yaml:"agent"`
	ShellTimeout        time.Duration  `yaml:"shell_timeout"`
	ShellOutputBytes    int            `yaml:"shell_output_bytes"`
	ShellPathPrefix     string         `yaml:"shell_path_prefix"`
	ModelTimeout        time.Duration  `yaml:"model_timeout"`
	ModelRequestTimeout time.Duration  `yaml:"model_request_timeout"`
	// ModelReasoningEffort 为空时使用模型默认强度；可选 low/medium/high/max。
	ModelReasoningEffort string `yaml:"model_reasoning_effort"`
	// SystemPromptTemplate 选择 internal/agent/prompts/<name>.md 模板；
	// 为空时使用默认的 coding 模板。
	SystemPromptTemplate string `yaml:"system_prompt_template"`
	PreviewBytes         int    `yaml:"preview_bytes"`
}

// resolveDatabaseDriver 决定使用哪种数据库方言。
// 优先 EULER_DATABASE_DRIVER 环境变量；留空时从 databaseURL 推断：
//   - "postgres://" 或 "postgresql://" 前缀 → postgres
//   - 其他（文件路径、file: URI）→ sqlite
func resolveDatabaseDriver(databaseURL string) string {
	if driver := os.Getenv("EULER_DATABASE_DRIVER"); driver != emptyValue {
		return driver
	}
	if strings.HasPrefix(databaseURL, "postgres://") ||
		strings.HasPrefix(databaseURL, "postgresql://") {
		return "postgres"
	}
	return "sqlite"
}

// Load 在内置默认值之上读取 path 指定的文件，从 EULER_DATABASE_URL
// 填充数据库 URL，并校验结果。文件缺失不算错误。
func Load(path string) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("home: %w", err)
	}
	cfg := Default()
	cfg.Listen = defaultListenAddr
	cfg.WorkspaceRoot = filepath.Join(home, "EulerWorkspaces")
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
		// 零配置启动：未显式指定时默认使用用户主目录下的 SQLite 单文件，
		// 父目录由 SQLite 方言的 Prepare 在打开前自动创建。
		cfg.DatabaseURL = filepath.Join(home, ".euler", "euler.db")
	}
	cfg.DatabaseDriver = resolveDatabaseDriver(cfg.DatabaseURL)
	cfg.loadLangfuseEnv()
	if err = cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Default 返回内置默认配置，不读取文件、环境或数据库地址。供没有服务端
// 的一次性运行（例如容器内评测）复用同一套限额与超时默认值。
func Default() Config {
	return Config{
		Listen: defaultListenAddr,
		Agent: AgentLimits{
			MaxModelRounds: defaultMaxRounds,
			MaxToolCalls:   defaultMaxToolCalls,
			MaxDuration:    defaultMaxDuration,
			MaxOutputBytes: defaultMaxOutput,
		},
		ShellTimeout:         defaultShellTimeout,
		ShellOutputBytes:     defaultShellOutput,
		ModelTimeout:         defaultModelTimeout,
		ModelRequestTimeout:  defaultModelRequestTimeout,
		SystemPromptTemplate: defaultPrompt,
		PreviewBytes:         defaultPreview,
		Langfuse:             LangfuseConfig{Environment: defaultLangfuseEnv},
	}
}

// LangfuseFromEnv 只从环境变量读取 Langfuse 配置，供没有配置文件的运行
// 使用；未设置 host 时返回零值，表示关闭追踪。
func LangfuseFromEnv() LangfuseConfig {
	cfg := Config{Langfuse: LangfuseConfig{Environment: defaultLangfuseEnv}}
	cfg.loadLangfuseEnv()
	return cfg.Langfuse
}

func (c Config) validate() error {
	if err := c.Agent.validate(); err != nil {
		return err
	}
	return c.validateRuntime()
}

func (l AgentLimits) validate() error {
	if l.MaxModelRounds < noAgentLimit || l.MaxToolCalls < noAgentLimit ||
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
	if c.ModelRequestTimeout <= noDuration {
		return errors.New("model_request_timeout must be positive")
	}
	if c.ModelReasoningEffort != emptyValue && !validReasoningEffort[c.ModelReasoningEffort] {
		return errors.New("model_reasoning_effort must be low, medium, high or max")
	}
	if !validPromptName(c.SystemPromptTemplate) {
		return errors.New("system_prompt_template must match ^[a-z0-9_-]+$")
	}
	return c.Langfuse.validate()
}

// loadLangfuseEnv 用环境变量覆盖 Langfuse 配置，兼容 EULER_ 前缀与
// Langfuse 官方 SDK 的标准变量名。
func (c *Config) loadLangfuseEnv() {
	c.Langfuse.Host = envOr(
		c.Langfuse.Host, "EULER_LANGFUSE_HOST", "LANGFUSE_BASE_URL", "LANGFUSE_HOST",
	)
	c.Langfuse.PublicKey = envOr(
		c.Langfuse.PublicKey, "EULER_LANGFUSE_PUBLIC_KEY", "LANGFUSE_PUBLIC_KEY",
	)
	c.Langfuse.SecretKey = envOr(
		c.Langfuse.SecretKey, "EULER_LANGFUSE_SECRET_KEY", "LANGFUSE_SECRET_KEY",
	)
}

// envOr 返回第一个非空环境变量的值；全部为空时返回 fallback。
func envOr(fallback string, names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != emptyValue {
			return value
		}
	}
	return fallback
}

// validate 要求 Langfuse 的三项参数同时提供；全部为空表示关闭追踪。
func (l LangfuseConfig) validate() error {
	if l.Host == emptyValue && l.PublicKey == emptyValue && l.SecretKey == emptyValue {
		return nil
	}
	if l.Host == emptyValue || l.PublicKey == emptyValue || l.SecretKey == emptyValue {
		return errors.New("langfuse needs host, public_key and EULER_LANGFUSE_SECRET_KEY together")
	}
	if !strings.HasPrefix(l.Host, "http://") && !strings.HasPrefix(l.Host, "https://") {
		return errors.New("langfuse host must start with http:// or https://")
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
