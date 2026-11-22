package agent

import (
	"fmt"

	"github.com/cloudwego/eino/components/tool"

	"euler/internal/tools"
	"euler/internal/workspace"
)

// buildTools 组装单次 run 的工具集：绑定到该 run 工作区的全新 FileStore、
// 拥有独立调用预算的全新 registry，并从 shell 执行中剥离所有模型凭据的
// 环境变量引用。parts 用于把工具调用按发生顺序追加到助手消息片段。
func (rt *Runtime) buildTools(
	input RunInput, parts tools.PartRecorder,
) ([]tool.BaseTool, error) {
	registry := tools.NewRegistry(tools.RegistryDeps{
		Calls:          input.Calls,
		Events:         rt.deps.Events,
		Gate:           rt.deps.Gate,
		Parts:          parts,
		RunID:          input.Request.RunID,
		MaxToolCalls:   rt.deps.Limits.MaxToolCalls,
		MaxOutputBytes: rt.deps.Limits.MaxOutputBytes,
		APIKey:         input.Model.APIKey,
	})
	built, err := registry.Tools(
		workspace.NewFileStore(input.Dir, rt.deps.PreviewBytes),
		tools.NewShellExecutor(tools.ShellConfig{
			Timeout: rt.deps.ShellTimeout, MaxOutputBytes: rt.deps.ShellOutputBytes,
			PathPrefix: rt.deps.ShellPathPrefix,
		}),
		input.Dir, input.Secrets,
	)
	if err != nil {
		return nil, fmt.Errorf("build tools: %w", err)
	}
	return built, nil
}
