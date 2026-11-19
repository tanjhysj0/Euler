package workspace

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// runGit 在 dir 中执行 git 命令。
func runGit(dir string, args ...string) error {
	_, err := outputGit(dir, args...)
	return err
}

// outputGit 在 dir 中执行 git 命令并返回 stdout。
func outputGit(dir string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", dir}, args...)
	//nolint:gosec // 参数全部由评测器构造，不包含用户输入
	cmd := exec.Command("git", fullArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
