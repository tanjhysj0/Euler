package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/event"
)

// recordingSink 记录检查器写出的助手文本与思考过程。
type recordingSink struct{ text, reasoning string }

func (s *recordingSink) add(_ context.Context, content string) error {
	s.text += content
	return nil
}

func (s *recordingSink) addReasoning(_ context.Context, content string) error {
	s.reasoning += content
	return nil
}

func (*recordingSink) flush(context.Context) error { return nil }

// recordingPublisher 记录 deltaWriter 追加的事件类型。
type recordingPublisher struct{ types []string }

func (p *recordingPublisher) Append(
	_ context.Context, runID uuid.UUID, eventType string, payload any,
) (event.Event, error) {
	p.types = append(p.types, eventType)
	return event.Event{ID: uuid.New(), RunID: runID, Type: eventType}, nil
}

func (*recordingPublisher) ListAfter(
	context.Context, uuid.UUID, int64,
) ([]event.Event, error) {
	return nil, nil
}

func (*recordingPublisher) Subscribe(uuid.UUID) (<-chan event.Event, func()) {
	return nil, func() {}
}

// noopRecorder 忽略正文落库，测试只关注思考事件的发送时机。
type noopRecorder struct{}

func (noopRecorder) UpdateContent(context.Context, uuid.UUID, string) error { return nil }

// TestDeltaWriterStreamsReasoningInBatches 验证思考过程边生成边发送：
// 未满批次不发送，满批次立即发送，末尾不足一批在 flush 时补发。
func TestDeltaWriterStreamsReasoningInBatches(t *testing.T) {
	publisher := &recordingPublisher{}
	writer := &deltaWriter{
		messages:  noopRecorder{},
		events:    publisher,
		runID:     uuid.New(),
		messageID: uuid.New(),
	}
	remaining := strings.Repeat("思", deltaBatchRunes-1)
	require.NoError(t, writer.addReasoning(context.Background(), remaining))
	require.Empty(t, publisher.types)
	require.NoError(t, writer.addReasoning(context.Background(), "考"))
	require.Equal(t, []string{"message.reasoning"}, publisher.types)
	require.NoError(t, writer.addReasoning(context.Background(), "收尾"))
	require.NoError(t, writer.flush(context.Background()))
	require.Equal(t, []string{"message.reasoning", "message.reasoning"}, publisher.types)
}

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

// TestForwardAssistantKeepsReasoning 要求思考增量写入 sink，且不影响
// 正文与工具调用判定。
func TestForwardAssistantKeepsReasoning(t *testing.T) {
	sink := &recordingSink{}
	found, err := forwardAssistant(context.Background(), &schema.Message{
		Role:             schema.Assistant,
		ReasoningContent: "先分析需求。",
		Content:          "最终答案。",
	}, sink)
	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, "先分析需求。", sink.reasoning)
	require.Equal(t, "最终答案。", sink.text)
}
