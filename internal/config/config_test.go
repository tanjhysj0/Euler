package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:8080" {
		t.Fatalf("listen=%q", cfg.Listen)
	}
	if cfg.DatabaseURL == "" || cfg.Agent.MaxModelRounds != 12 {
		t.Fatalf("cfg=%+v", cfg)
	}
	if cfg.ModelTimeout != 2*time.Minute {
		t.Fatalf("model_timeout=%v", cfg.ModelTimeout)
	}
	if !strings.HasSuffix(cfg.WorkspaceRoot, "EulerWorkspaces") {
		t.Fatal(cfg.WorkspaceRoot)
	}
}

func TestLoadRejectsInvalidLimits(t *testing.T) {
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := []byte("agent:\n  max_model_rounds: 0\n  max_tool_calls: 40\n" +
		"  max_duration: 15m\n  max_output_bytes: 65536\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoadParsesAgentDuration(t *testing.T) {
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("agent:\n  max_duration: 15m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.MaxDuration != 15*time.Minute {
		t.Fatalf("max_duration = %v", cfg.Agent.MaxDuration)
	}
}

func TestLoadRejectsUnknownReasoningEffort(t *testing.T) {
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("model_reasoning_effort: turbo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoadRejectsRelativeShellPathPrefix(t *testing.T) {
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("shell_path_prefix: runtime/bin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error")
	}
}

// clearLangfuseEnv 清空环境里的 Langfuse 配置，保证测试不受外部环境影响。
func clearLangfuseEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"EULER_LANGFUSE_HOST", "EULER_LANGFUSE_PUBLIC_KEY", "EULER_LANGFUSE_SECRET_KEY",
		"LANGFUSE_BASE_URL", "LANGFUSE_HOST", "LANGFUSE_PUBLIC_KEY", "LANGFUSE_SECRET_KEY",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadParsesLangfuse(t *testing.T) {
	clearLangfuseEnv(t)
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	t.Setenv("EULER_LANGFUSE_SECRET_KEY", "sk-lf-test")
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "langfuse:\n  host: https://cloud.langfuse.com\n  public_key: pk-lf-test\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Langfuse.Enabled() || cfg.Langfuse.SecretKey != "sk-lf-test" {
		t.Fatalf("langfuse = %+v", cfg.Langfuse)
	}
}

func TestLoadLangfuseFromEnvironment(t *testing.T) {
	clearLangfuseEnv(t)
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	t.Setenv("LANGFUSE_BASE_URL", "https://jp.cloud.langfuse.com")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "pk-lf-test")
	t.Setenv("LANGFUSE_SECRET_KEY", "sk-lf-test")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Langfuse.Enabled() || cfg.Langfuse.SecretKey != "sk-lf-test" {
		t.Fatalf("langfuse = %+v", cfg.Langfuse)
	}
}

func TestLoadRejectsIncompleteLangfuse(t *testing.T) {
	clearLangfuseEnv(t)
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("langfuse:\n  host: https://cloud.langfuse.com\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected incomplete langfuse error")
	}
}

func TestLoadRejectsLangfuseHostWithoutScheme(t *testing.T) {
	clearLangfuseEnv(t)
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	t.Setenv("EULER_LANGFUSE_SECRET_KEY", "sk-lf-test")
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "langfuse:\n  host: cloud.langfuse.com\n  public_key: pk-lf-test\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected langfuse host scheme error")
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("EULER_DATABASE_URL", "")
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected missing database URL error")
	}
}

func TestLoadRejectsNonLoopbackListen(t *testing.T) {
	t.Setenv("EULER_DATABASE_URL", "postgres://euler:euler@127.0.0.1/euler?sslmode=disable")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("listen: 0.0.0.0:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected loopback validation error")
	}
}
