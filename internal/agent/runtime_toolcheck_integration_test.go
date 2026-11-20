//go:build integration

package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"

	"euler/internal/persistence"
)

// TestRuntimeExecutesToolCallAfterText 用真实 ReAct 图验证：模型先流式输出
// 文本、随后在同一轮输出工具调用时，工具仍会被执行并进入下一轮。
func TestRuntimeExecutesToolCallAfterText(t *testing.T) {
	fixture := newRuntimeFixture(t)
	index := 0
	chatModel := &fakeReactModel{rounds: [][]*schema.Message{
		{
			{Role: schema.Assistant, Content: "我先查看工作区。"},
			{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				Index: &index, ID: "call_1", Type: "function",
				Function: schema.FunctionCall{Name: "file_list", Arguments: `{}`},
			}}},
		},
		{schema.AssistantMessage("完成", nil)},
	}}
	runtime := NewRuntime(fixture.deps(t, chatModel))
	require.NoError(t, runtime.Run(context.Background(), fixture.request))
	var calls []persistence.ToolCall
	require.NoError(t, fixture.db.Where("run_id = ?", fixture.request.RunID).
		Find(&calls).Error)
	require.Len(t, calls, 1)
	require.Equal(t, "file_list", calls[0].Name)
	require.Equal(t, "succeeded", calls[0].Status)
	var message persistence.Message
	require.NoError(t, fixture.db.First(&message,
		"id = ?", fixture.request.AssistantMessageID).Error)
	require.Contains(t, message.Content, "完成")
}

// TestRuntimeSubmitEndsRunWithoutAnotherModelRound 要求模型调用 submit 后
// 立即结束：不进入下一轮模型调用，也不会因预算耗尽而失败。
func TestRuntimeSubmitEndsRunWithoutAnotherModelRound(t *testing.T) {
	fixture := newRuntimeFixture(t)
	index := 0
	chatModel := &fakeReactModel{rounds: [][]*schema.Message{
		{
			{Role: schema.Assistant, Content: "验收点全部通过。"},
			{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				Index: &index, ID: "call_submit", Type: "function",
				Function: schema.FunctionCall{
					Name: "submit", Arguments: `{"summary":"验收点全部通过"}`,
				},
			}}},
		},
	}}
	runtime := NewRuntime(fixture.deps(t, chatModel))
	require.NoError(t, runtime.Run(context.Background(), fixture.request))
	var calls []persistence.ToolCall
	require.NoError(t, fixture.db.Where("run_id = ?", fixture.request.RunID).
		Find(&calls).Error)
	require.Len(t, calls, 1)
	require.Equal(t, "submit", calls[0].Name)
	require.Equal(t, "succeeded", calls[0].Status)
}
