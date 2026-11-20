package tools

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
)

// submitToolName 是显式结束 run 的提交工具名；运行时据此配置 ReAct 的
// ToolReturnDirectly，使模型提交后不再进入下一轮模型调用。
const submitToolName = "submit"

type submitInput struct {
	Summary string `json:"summary" jsonschema:"description=验收结论摘要，逐条说明验收点与验证结果,required"`
}

// SubmitToolName 返回提交工具的名称，供运行时配置直接返回分支。
func SubmitToolName() string { return submitToolName }

// submitTool 让模型显式声明任务已完成，避免它在预算耗尽前反复自测。
func (r *Registry) submitTool() (tool.BaseTool, error) {
	return registerTool(r, submitToolName,
		"提交任务结果并结束本次运行。仅在验收点全部通过后调用一次，调用后不再继续修改或测试。",
		submitOutcome)
}

func submitOutcome(_ context.Context, input submitInput) (toolOutcome, error) {
	return completedOutcome(map[string]any{"summary": input.Summary}), nil
}
