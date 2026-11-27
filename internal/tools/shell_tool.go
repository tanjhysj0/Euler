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

// shellToolset 将 shell 工具绑定到单个执行器和工作区目录。
type shellToolset struct {
	registry *Registry
	executor Executor
	dir      string
	secrets  []string
}

func (r *Registry) shellTool(
	executor Executor, dir string, secrets []string,
) (tool.BaseTool, error) {
	set := shellToolset{registry: r, executor: executor, dir: dir, secrets: secrets}
	return registerTool(r, "shell", "在工作区执行命令（需用户批准）", set.run)
}

// run 只准备受审批保护的调用；实际执行发生在审批关卡放行后的 apply 中。
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
