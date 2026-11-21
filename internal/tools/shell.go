// Package tools 实现 agent 可调用的宿主侧工具。
package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"euler/internal/approval"
)

const (
	// loginFlag 以登录 shell 启动，使用户的 PATH 设置生效；仅 zsh/bash 支持。
	loginFlag = "-l"
	// stdinFlag 让 shell 从标准输入读取命令。
	stdinFlag = "-s"
	// fallbackShell 是候选解释器都不存在时的兜底选择。
	fallbackShell = "/bin/sh"
	// darwinGOOS 是 macOS 的 GOOS 取值，用于保留本地登录 shell 行为。
	darwinGOOS = "darwin"
	// envAssignment 分隔环境变量的键与值。
	envAssignment = "="
	// emptyCapacity 用于以预设长度构建切片。
	emptyCapacity = 0
	// noTimeout 表示“沿用配置的截止时间”。
	noTimeout time.Duration = 0
	// emptyText 表示未配置的可选字符串。
	emptyText = ""
)

// shellSpec 描述一个候选解释器及其是否支持登录模式。
type shellSpec struct {
	path  string
	login bool
}

// shellCandidates 返回当前平台优先使用的解释器顺序。评测任务跑在 Linux
// 容器里，因此以 bash/sh 为主；macOS 上保留 zsh 登录 shell 以读取用户 PATH。
func shellCandidates() []shellSpec {
	if runtime.GOOS == darwinGOOS {
		return []shellSpec{
			{path: "/bin/zsh", login: true},
			{path: "/bin/bash", login: true},
			{path: "/bin/sh"},
		}
	}
	return []shellSpec{
		{path: "/bin/bash", login: true},
		{path: "/bin/sh"},
	}
}

var (
	// ErrUnsupportedPlatform 标记当前平台没有适配器的宿主执行。
	ErrUnsupportedPlatform = errors.New("shell execution unsupported on this platform")
)

// ShellConfig 为单个执行器的每条命令设定限制。
type ShellConfig struct {
	Timeout        time.Duration
	MaxOutputBytes int
	// PathPrefix 非空时会在登录 shell 完成初始化后置于 PATH 最前，
	// 用于让 python 等运行时与判分环境保持一致。
	PathPrefix string
}

// ShellRequest 表示一条宿主命令。Dir 是审批卡片中展示的工作区目录；
// 它只是便利信息，并非安全边界。Timeout 仅对本条命令覆盖配置的截止时间，
// SecretNames 列出引用模型凭据、需要剔除的环境变量。
type ShellRequest struct {
	Dir         string        `json:"dir,omitempty"`
	Command     string        `json:"command"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	SecretNames []string      `json:"secret_names,omitempty"`
}

// ShellResult 是一条命令的执行结果。Err 是解释执行失败或中断的 Go 错误：
// 超时触发时为 context.DeadlineExceeded，调用方放弃时为 context.Canceled，
// 否则为启动失败的错误。
type ShellResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
	Err       error  `json:"-"`
}

// GuardedCall 将一个审批请求与其授权的命令配对。
type GuardedCall struct {
	Approval approval.Request
	Shell    ShellRequest
}

// Shell 通过系统登录 shell 执行宿主命令。
type Shell struct{ config ShellConfig }

// NewShellExecutor 根据配置构建一个 Shell。
func NewShellExecutor(config ShellConfig) *Shell { return &Shell{config: config} }

// Execute 在 request.Dir 内，把命令作为从标准输入读取的 shell 脚本运行，
// 并从子进程环境中剔除所有凭据引用。超时或取消会杀死整个进程组，
// 合并输出会在配置的最大值处截断。
func (s *Shell) Execute(ctx context.Context, request ShellRequest) ShellResult {
	shellCtx, cancel := context.WithTimeout(ctx, s.timeout(request.Timeout))
	defer cancel()
	output := &limitedWriter{limit: s.config.MaxOutputBytes}
	shell, args := resolveShell(shellExists)
	cmd := exec.CommandContext(shellCtx, shell, args...) //nolint:gosec // 解释器来自固定候选列表
	cmd.Dir = request.Dir
	cmd.Env = SanitizeEnvironment(os.Environ(), request.SecretNames)
	cmd.Stdin = strings.NewReader(s.script(request.Command))
	cmd.Stdout, cmd.Stderr = output, output
	if err := configureProcessGroup(cmd); err != nil {
		return ShellResult{Err: err}
	}
	return collect(shellCtx, output, cmd.Run())
}

// resolveShell 按当前平台优先级返回实际存在的解释器及启动参数；
// 登录参数只对支持 -l 的 zsh/bash 使用。
func resolveShell(exists func(string) bool) (string, []string) {
	return resolveShellFrom(shellCandidates(), exists)
}

// resolveShellFrom 从候选列表中选出第一个存在的解释器；exists 便于测试。
func resolveShellFrom(candidates []shellSpec, exists func(string) bool) (string, []string) {
	for _, candidate := range candidates {
		if !exists(candidate.path) {
			continue
		}
		if candidate.login {
			return candidate.path, []string{loginFlag, stdinFlag}
		}
		return candidate.path, []string{stdinFlag}
	}
	return fallbackShell, []string{stdinFlag}
}

// shellExists 判断候选解释器是否为可用的普通文件。
func shellExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// RunGuarded 先向 gate 请求裁决，只有在获批后才启动命令，
// 因此被拒绝或取消的调用不会在磁盘上留下任何副作用。
func (s *Shell) RunGuarded(
	ctx context.Context,
	gate approval.Gate,
	call GuardedCall,
) (ShellResult, approval.Decision, error) {
	decision, err := gate.Require(ctx, call.Approval)
	if err != nil {
		return ShellResult{}, decision, err
	}
	if !decision.Approved {
		return ShellResult{}, decision, nil
	}
	return s.Execute(ctx, call.Shell), decision, nil
}

// script 在配置了 PathPrefix 时把前缀目录放到 PATH 最前；登录 shell 已读取完
// 用户配置，因此这里设置的优先级不会被 profile 覆盖。
func (s *Shell) script(command string) string {
	if s.config.PathPrefix == emptyText {
		return command
	}
	return "export PATH=" + quoteForShell(s.config.PathPrefix) + ":$PATH\n" + command
}

// quoteForShell 用单引号包裹路径，供 shell 安全展开。
func quoteForShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// timeout 优先使用单条命令的截止时间，否则回退到配置值。
func (s *Shell) timeout(requested time.Duration) time.Duration {
	if requested > noTimeout {
		return requested
	}
	return s.config.Timeout
}

// collect 解读进程的执行结果以及其 context 结束的原因。
func collect(ctx context.Context, output *limitedWriter, runErr error) ShellResult {
	result := ShellResult{Output: output.String(), Truncated: output.Truncated()}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.TimedOut, result.Err = true, context.DeadlineExceeded
		return result
	case errors.Is(ctx.Err(), context.Canceled):
		result.Err = context.Canceled
		return result
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		result.ExitCode = exit.ExitCode()
		return result
	}
	result.Err = runErr
	return result
}

// SanitizeEnvironment 丢弃所有键名为凭据引用的条目，
// 同时保留其余条目的顺序。
func SanitizeEnvironment(environ, secretNames []string) []string {
	blocked := make(map[string]struct{}, len(secretNames))
	for _, name := range secretNames {
		blocked[name] = struct{}{}
	}
	filtered := make([]string, emptyCapacity, len(environ))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, envAssignment)
		if _, found := blocked[key]; found {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// limitedWriter 保留任一流写入的前 limit 个字节，并只报告一次截断。
// 它始终声称写入了整个缓冲区：exec 从管道复制数据，
// 短写会导致 io.ErrShortWrite。
type limitedWriter struct {
	mu        sync.Mutex
	buffer    []byte
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - len(w.buffer)
	if remaining >= len(p) {
		w.buffer = append(w.buffer, p...)
		return len(p), nil
	}
	if remaining > emptyCapacity {
		w.buffer = append(w.buffer, p[:remaining]...)
	}
	w.truncated = true
	return len(p), nil
}

// String 返回捕获的字节内容。
func (w *limitedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buffer)
}

// Truncated 报告是否有输出被丢弃。
func (w *limitedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

var _ io.Writer = (*limitedWriter)(nil)
