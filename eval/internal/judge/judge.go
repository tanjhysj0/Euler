// Package judge 负责在干净基线上应用补丁并判定测试结果。
package judge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"euler/eval/internal/report"
	"euler/eval/internal/task"
	"euler/eval/internal/workspace"
)

const (
	defaultBuildTimeout = 2 * time.Minute
	defaultTestTimeout  = 5 * time.Minute
)

// Options 控制判分环境。
type Options struct {
	FixtureDir   string
	WorkRoot     string
	KeepWorkdir  bool
	BuildTimeout time.Duration
	TestTimeout  time.Duration
}

// Result 是单题判分结果。
type Result struct {
	InstanceID string
	Resolved   bool
	Reason     string
	BuildOK    bool
	FailToPass report.TestSummary
	PassToPass report.TestSummary
}

// Run 执行单题判分。
func Run(t task.Task, modelPatch string, opts Options) (Result, error) {
	result := Result{InstanceID: t.ID}
	if strings.TrimSpace(modelPatch) == "" {
		result.Reason = "no_changes"
		return result, nil
	}
	workDir := filepath.Join(opts.WorkRoot, "judge-"+t.ID)
	if err := os.RemoveAll(workDir); err != nil {
		return result, err
	}
	if !opts.KeepWorkdir {
		defer func() { _ = os.RemoveAll(workDir) }()
	}
	repo, err := workspace.Materialize(opts.FixtureDir, workDir, t.BugPatch())
	if err != nil {
		return result, err
	}
	patchFile, err := writeTempPatch(t.ID, modelPatch)
	if err != nil {
		return result, err
	}
	defer func() { _ = os.Remove(patchFile) }()
	if err = repo.ApplyFile(patchFile); err != nil {
		result.Reason = "patch_failed"
		return result, nil
	}
	for _, path := range t.ProtectedTests {
		if restoreErr := repo.Restore(path); restoreErr != nil {
			return result, restoreErr
		}
	}
	if _, err = repo.RemoveNewTestFiles(); err != nil {
		return result, err
	}
	if err = repo.ApplyFile(t.TestPatch()); err != nil {
		return result, fmt.Errorf("应用隐藏测试: %w", err)
	}
	buildOK, buildErr := runBuild(repo.Dir, timeoutOr(opts.BuildTimeout, defaultBuildTimeout))
	result.BuildOK = buildOK
	if buildErr != nil {
		return result, buildErr
	}
	if !buildOK {
		result.Reason = "build_failed"
		return result, nil
	}
	output, testErr := runTestCommand(repo.Dir, t.TestCommand,
		timeoutOr(opts.TestTimeout, defaultTestTimeout))
	if testErr != nil {
		return result, testErr
	}
	outcomes := parseTestOutput(bytes.NewReader(output))
	result.FailToPass = summarizeTests(t.FailToPass, outcomes)
	result.PassToPass = summarizeTests(t.PassToPass, outcomes)
	if len(result.FailToPass.Failed) > 0 || len(result.PassToPass.Failed) > 0 {
		result.Reason = "tests_failed"
		return result, nil
	}
	result.Resolved = true
	return result, nil
}

func timeoutOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func writeTempPatch(id, content string) (string, error) {
	file, err := os.CreateTemp("", "euleval-"+id+"-*.patch")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err = file.WriteString(content); err != nil {
		_ = file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// runBuild 运行 go build，返回是否成功。
func runBuild(dir string, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("go build 超时: %w", ctx.Err())
		}
		return false, nil
	}
	return true, nil
}

// runTestCommand 执行题目指定的测试命令并返回 stdout。
func runTestCommand(dir, command string, timeout time.Duration) ([]byte, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("test_command 为空")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	//nolint:gosec // 命令来自题库，属于受信输入
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("测试超时: %w", ctx.Err())
		}
		// 测试失败本身是正常结果，继续解析输出。
	}
	return stdout.Bytes(), nil
}
