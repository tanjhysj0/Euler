//go:build darwin || linux

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestShellKillsProcessGroupOnCancel 验证取消时整个进程组都被杀死。
// 该测试依赖 syscall.Kill 与 POSIX 进程组语义，因此只在 darwin/linux 运行。
func TestShellKillsProcessGroupOnCancel(t *testing.T) {
	dir := t.TempDir()
	executor := NewShellExecutor(ShellConfig{Timeout: 30 * time.Second, MaxOutputBytes: 1024})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan ShellResult, 1)
	go func() {
		resultCh <- executor.Execute(ctx, ShellRequest{
			Dir: dir, Command: "sleep 30 & echo $! > child.pid; wait",
		})
	}()
	pid := waitForChildPID(t, filepath.Join(dir, "child.pid"))
	cancel()
	result := <-resultCh
	require.ErrorIs(t, result.Err, context.Canceled)
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}, 5*time.Second, 20*time.Millisecond)
}

// waitForChildPID 轮询直到后台子进程写入完整的 pid。
func waitForChildPID(t *testing.T, pidFile string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		content, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
		if parseErr != nil {
			return false
		}
		pid = parsed
		return true
	}, 5*time.Second, 10*time.Millisecond)
	return pid
}
