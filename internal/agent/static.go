package agent

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/schema"

	"euler/internal/run"
	"euler/internal/tools"
)

// ErrMissingInstruction 表示一次性运行没有提供用户指令。
var ErrMissingInstruction = errors.New("agent: missing instruction")

// ErrMissingWorkspace 表示一次性运行没有指定工作区目录。
var ErrMissingWorkspace = errors.New("agent: missing workspace dir")

// StaticLoader 为不依赖数据库的一次性运行提供输入：一条用户指令、一个
// 工作区目录和一组已确定的模型参数。system prompt 与工具装配仍复用服务端
// 路径，因此容器内执行与 Web 端行为一致。
type StaticLoader struct {
	Dir      string
	Prompt   string
	Model    ModelSpec
	Calls    tools.CallRecorder
	Messages MessageRecorder
}

// 确保 StaticLoader 满足 Runtime 的输入端口。
var _ InputLoader = StaticLoader{}

// Load 组装单轮历史：system prompt 与本次用户指令，不含任何此前对话。
func (l StaticLoader) Load(_ context.Context, request run.Request) (RunInput, error) {
	if l.Dir == "" {
		return RunInput{}, ErrMissingWorkspace
	}
	if request.Input == "" {
		return RunInput{}, ErrMissingInstruction
	}
	prompt, err := renderSystemPrompt(l.Prompt, l.Dir)
	if err != nil {
		return RunInput{}, err
	}
	return RunInput{
		Request: request,
		History: []*schema.Message{
			schema.SystemMessage(prompt), schema.UserMessage(request.Input),
		},
		Model: l.Model, Dir: l.Dir,
		Calls: l.Calls, Messages: l.Messages,
	}, nil
}
