package tools

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/eino/components/tool"
)

const exitSuccess = 0

type shellInput struct {
	Command        string `json:"command" jsonschema:"description=要执行的 shell 命令,required"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"description=可选的命令超时秒数"`
}

// shellToolset binds the shell tool to one executor and workspace directory.
type shellToolset struct {
	registry *Registry
	executor *Shell
	dir      string
	secrets  []string
}

func (r *Registry) shellTool(
	executor *Shell, dir string, secrets []string,
) (tool.BaseTool, error) {
	set := shellToolset{registry: r, executor: executor, dir: dir, secrets: secrets}
	return registerTool(r, "shell", "在工作区执行 shell 命令（需用户批准）", set.run)
}

// run only prepares the guarded call; execution happens in apply after the
// approval gate grants it.
func (s shellToolset) run(_ context.Context, input shellInput) (toolOutcome, error) {
	request := ShellRequest{
		Dir: s.dir, Command: input.Command,
		Timeout:     time.Duration(input.TimeoutSeconds) * time.Second,
		SecretNames: s.secrets,
	}
	return toolOutcome{
		payload: map[string]any{
			"command": input.Command, "dir": s.dir, "timeout_seconds": input.TimeoutSeconds,
		},
		apply: func(ctx context.Context) (toolOutcome, error) {
			result := s.executor.Execute(ctx, request)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return toolOutcome{}, ctxErr
			}
			return s.resultOutcome(result), nil
		},
	}, nil
}

func (s shellToolset) resultOutcome(result ShellResult) toolOutcome {
	fields := map[string]any{
		"output":    s.registry.truncate(result.Output),
		"exit_code": result.ExitCode,
		"timed_out": result.TimedOut,
		"truncated": result.Truncated,
	}
	switch {
	case result.Err == nil && result.ExitCode == exitSuccess:
		return completedOutcome(fields)
	case result.TimedOut || errors.Is(result.Err, context.DeadlineExceeded):
		fields["error"] = shellError("shell_timeout", "命令执行超时")
	case result.Err != nil:
		fields["error"] = shellError("shell_error", "命令执行失败")
	default:
		fields["error"] = shellError("shell_exit", "命令退出码非零")
	}
	return failedOutcome(fields)
}

func shellError(code, message string) map[string]any {
	return map[string]any{"code": code, "message": message}
}
