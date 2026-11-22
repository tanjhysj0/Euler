package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/approval"
)

// fakeGate 用固定的裁决回应每一个审批请求。
type fakeGate struct {
	decision approval.Decision
	err      error
	calls    int
	requests []approval.Request
}

func (g *fakeGate) Require(
	_ context.Context, request approval.Request,
) (approval.Decision, error) {
	g.calls++
	g.requests = append(g.requests, request)
	return g.decision, g.err
}

func TestSanitizeEnvironmentRemovesModelSecrets(t *testing.T) {
	got := SanitizeEnvironment(
		[]string{"PATH=/bin", "OPENAI_API_KEY=secret", "HOME=/tmp"},
		[]string{"OPENAI_API_KEY"},
	)
	require.Equal(t, []string{"PATH=/bin", "HOME=/tmp"}, got)
}

func TestShellTimeoutAndTruncation(t *testing.T) {
	// 在真实主机上登录 shell 的启动耗时超过 50ms，因此截断分支
	// 使用宽松的截止时间；只有超时分支使用 50ms。
	truncating := NewShellExecutor(ShellConfig{Timeout: 10 * time.Second, MaxOutputBytes: 8})
	result := truncating.Execute(context.Background(),
		ShellRequest{Dir: t.TempDir(), Command: "printf 123456789"})
	require.NoError(t, result.Err)
	require.True(t, result.Truncated)
	require.Equal(t, "12345678", result.Output)
	impatient := NewShellExecutor(ShellConfig{Timeout: 50 * time.Millisecond, MaxOutputBytes: 8})
	result = impatient.Execute(context.Background(),
		ShellRequest{Dir: t.TempDir(), Command: "sleep 5"})
	require.ErrorIs(t, result.Err, context.DeadlineExceeded)
	require.True(t, result.TimedOut)
}

// TestResolveShellOrderFollowsPlatform 验证 Linux 优先 bash/sh、macOS 优先
// zsh，且登录参数只用于支持 -l 的 zsh/bash。
func TestResolveShellOrderFollowsPlatform(t *testing.T) {
	linuxOrder := []shellSpec{
		{path: "/bin/bash", login: true},
		{path: "/bin/sh"},
	}
	shell, args := resolveShellFrom(linuxOrder,
		func(path string) bool { return path == "/bin/bash" })
	require.Equal(t, "/bin/bash", shell)
	require.Equal(t, []string{loginFlag, stdinFlag}, args)

	shell, args = resolveShellFrom(linuxOrder,
		func(path string) bool { return path == "/bin/sh" })
	require.Equal(t, "/bin/sh", shell)
	require.Equal(t, []string{stdinFlag}, args)

	darwinOrder := []shellSpec{
		{path: "/bin/zsh", login: true},
		{path: "/bin/bash", login: true},
		{path: "/bin/sh"},
	}
	shell, args = resolveShellFrom(darwinOrder,
		func(path string) bool { return path == "/bin/zsh" })
	require.Equal(t, "/bin/zsh", shell)
	require.Equal(t, []string{loginFlag, stdinFlag}, args)

	shell, args = resolveShellFrom(darwinOrder,
		func(path string) bool { return path == "/bin/bash" })
	require.Equal(t, "/bin/bash", shell)
	require.Equal(t, []string{loginFlag, stdinFlag}, args)

	shell, args = resolveShellFrom(darwinOrder, func(string) bool { return false })
	require.Equal(t, fallbackShell, shell)
	require.Equal(t, []string{stdinFlag}, args)
}

// TestShellCandidatesPreferPlatformShell 验证非 darwin 平台优先 bash。
func TestShellCandidatesPreferPlatformShell(t *testing.T) {
	candidates := shellCandidates()
	require.NotEmpty(t, candidates)
	if runtime.GOOS == darwinGOOS {
		require.Equal(t, "/bin/zsh", candidates[0].path)
		return
	}
	require.Equal(t, "/bin/bash", candidates[0].path)
}

// TestResolveShellReturnsExistingBinary 验证真实环境下选中的解释器存在。
func TestResolveShellReturnsExistingBinary(t *testing.T) {
	shell, _ := resolveShell(shellExists)
	info, err := os.Stat(shell)
	require.NoError(t, err)
	require.False(t, info.IsDir())
}

func TestShellReportsExitCode(t *testing.T) {
	executor := NewShellExecutor(ShellConfig{Timeout: 10 * time.Second, MaxOutputBytes: 1024})
	result := executor.Execute(context.Background(),
		ShellRequest{Dir: t.TempDir(), Command: "exit 3"})
	require.NoError(t, result.Err)
	require.Equal(t, 3, result.ExitCode)
	require.False(t, result.TimedOut)
	require.False(t, result.Truncated)
}

func TestShellPathPrefixOverridesLoginPath(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "python3")
	require.NoError(t, os.WriteFile( //nolint:gosec // 测试垫片必须可执行
		shim, []byte("#!/bin/sh\necho shim-python\n"), 0o755,
	))
	executor := NewShellExecutor(ShellConfig{
		Timeout: 10 * time.Second, MaxOutputBytes: 1024, PathPrefix: dir,
	})
	result := executor.Execute(context.Background(), ShellRequest{
		Dir: t.TempDir(), Command: "python3 --version",
	})
	require.NoError(t, result.Err)
	require.Equal(t, "shim-python", strings.TrimSpace(result.Output))
}

func TestShellStripsSecretsFromChildEnvironment(t *testing.T) {
	t.Setenv("EULER_TEST_MODEL_KEY", "sk-shell-secret")
	executor := NewShellExecutor(ShellConfig{Timeout: 10 * time.Second, MaxOutputBytes: 1024})
	result := executor.Execute(context.Background(), ShellRequest{
		Dir: t.TempDir(), Command: "printf ${EULER_TEST_MODEL_KEY:-missing}",
		SecretNames: []string{"EULER_TEST_MODEL_KEY"},
	})
	require.NoError(t, result.Err)
	require.Equal(t, "missing", result.Output)
}

func TestShellCancelledContextHasNoSideEffect(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := NewShellExecutor(ShellConfig{Timeout: 10 * time.Second, MaxOutputBytes: 1024})
	result := executor.Execute(ctx, ShellRequest{Dir: dir, Command: "touch marker"})
	require.ErrorIs(t, result.Err, context.Canceled)
	require.NoFileExists(t, filepath.Join(dir, "marker"))
}

func guardedCall(dir, command string) GuardedCall {
	return GuardedCall{
		Approval: approval.Request{
			RunID: uuid.New(), ToolCallID: uuid.New(), Name: "shell",
			Payload: map[string]any{"command": command},
		},
		Shell: ShellRequest{Dir: dir, Command: command},
	}
}

func TestRunGuardedRejectedHasNoSideEffect(t *testing.T) {
	dir := t.TempDir()
	gate := &fakeGate{decision: approval.Decision{
		Approved: false, Status: approval.StatusRejected, Reason: "不允许",
	}}
	executor := NewShellExecutor(ShellConfig{Timeout: 10 * time.Second, MaxOutputBytes: 1024})
	result, decision, err := executor.RunGuarded(
		context.Background(), gate, guardedCall(dir, "touch marker"))
	require.NoError(t, err)
	require.False(t, decision.Approved)
	require.Equal(t, approval.StatusRejected, decision.Status)
	require.Empty(t, result.Output)
	require.NoFileExists(t, filepath.Join(dir, "marker"))
	require.Equal(t, 1, gate.calls)
}

func TestRunGuardedApprovedExecutes(t *testing.T) {
	gate := &fakeGate{decision: approval.Decision{
		Approved: true, Status: approval.StatusApproved,
	}}
	executor := NewShellExecutor(ShellConfig{Timeout: 10 * time.Second, MaxOutputBytes: 1024})
	result, decision, err := executor.RunGuarded(
		context.Background(), gate, guardedCall(t.TempDir(), "printf ok"))
	require.NoError(t, err)
	require.True(t, decision.Approved)
	require.NoError(t, result.Err)
	require.Equal(t, "ok", result.Output)
}

func TestRunGuardedGateErrorSkipsExecution(t *testing.T) {
	dir := t.TempDir()
	gate := &fakeGate{err: errors.New("approval store down")}
	executor := NewShellExecutor(ShellConfig{Timeout: 10 * time.Second, MaxOutputBytes: 1024})
	_, _, err := executor.RunGuarded(
		context.Background(), gate, guardedCall(dir, "touch marker"))
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(dir, "marker"))
}
