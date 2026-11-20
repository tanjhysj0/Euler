package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

// recordingSink 记录检查器写出的助手文本。
type recordingSink struct{ text string }

func (s *recordingSink) add(_ context.Context, content string) error {
	s.text += content
	return nil
}

func (*recordingSink) flush(context.Context) error { return nil }

// TestStreamHasToolCallsAfterText 要求先输出文本、后输出工具调用的流仍被
// 识别为工具调用，且文本已经写出；Eino 默认只看首个非空分片，会漏掉这种模型。
func TestStreamHasToolCallsAfterText(t *testing.T) {
	index := 0
	stream := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "我先查看工作区。"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			Index: &index, ID: "call_1", Type: "function",
			Function: schema.FunctionCall{Name: "file_list", Arguments: `{}`},
		}}},
	})
	sink := &recordingSink{}
	found, err := streamHasToolCalls(context.Background(), stream, sink)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "我先查看工作区。", sink.text)
}

// TestStreamHasToolCallsWithoutToolCalls 确认纯文本流仍判定为无工具调用。
func TestStreamHasToolCallsWithoutToolCalls(t *testing.T) {
	stream := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Assistant, Content: "已完成。"},
	})
	sink := &recordingSink{}
	found, err := streamHasToolCalls(context.Background(), stream, sink)
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, "已完成。", sink.text)
}
