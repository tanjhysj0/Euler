package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
