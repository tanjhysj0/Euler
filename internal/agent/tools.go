package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/tool"

	"euler/internal/modelprofile"
	"euler/internal/run"
	"euler/internal/tools"
	"euler/internal/workspace"
)

// buildTools 组装单次 run 的工具集：绑定到该 run 工作区的全新 FileStore、
// 拥有独立调用预算的全新 registry，并从 shell 执行中剥离所有模型凭据的
// 环境变量引用。
func (rt *Runtime) buildTools(
	ctx context.Context, request run.Request,
	profile modelprofile.ResolvedProfile, dir string,
) ([]tool.BaseTool, error) {
	secrets, err := rt.deps.Profiles.EnvironmentSecretNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("list environment secrets: %w", err)
	}
	registry := tools.NewRegistry(tools.RegistryDeps{
		DB: rt.deps.DB, Events: rt.deps.Events, Gate: rt.deps.Gate,
		RunID:          request.RunID,
		MaxToolCalls:   rt.deps.Limits.MaxToolCalls,
		MaxOutputBytes: rt.deps.Limits.MaxOutputBytes,
		APIKey:         profile.APIKey,
	})
	built, err := registry.Tools(
		workspace.NewFileStore(dir, rt.deps.PreviewBytes),
		tools.ShellConfig{
			Timeout: rt.deps.ShellTimeout, MaxOutputBytes: rt.deps.ShellOutputBytes,
		},
		dir, secrets,
	)
	if err != nil {
		return nil, fmt.Errorf("build tools: %w", err)
	}
	return built, nil
}
