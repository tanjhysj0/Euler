package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"euler/internal/event"
	"euler/internal/logging"
	"euler/internal/run"
)

// deltaBatchRunes：累计达到这么多 UTF-8 rune，或当前模型 stream 结束时，
// 就 flush 一次助手文本。
const (
	deltaBatchRunes = 128
	emptyPending    = 0
	emptyContent    = ""
)

// deltaWriter 将助手文本批量写入流式消息行和 message.delta 事件。
// 思考过程只发 message.reasoning 事件，不落库。
// 只有 future 消费者 goroutine 会访问它。
type deltaWriter struct {
	messages         MessageRecorder
	events           event.Publisher
	runID            uuid.UUID
	messageID        uuid.UUID
	apiKey           string
	full             strings.Builder
	pending          []rune
	reasoning        strings.Builder
	reasoningPending []rune
}

func newDeltaWriter(
	events event.Publisher, request run.Request, messages MessageRecorder, apiKey string,
) *deltaWriter {
	return &deltaWriter{
		messages: messages, events: events, runID: request.RunID,
		messageID: request.AssistantMessageID, apiKey: apiKey,
	}
}

// text 返回已累积的助手全文；只应在 stream 全部结束后读取。
func (w *deltaWriter) text() string {
	return w.full.String()
}

func (w *deltaWriter) add(ctx context.Context, content string) error {
	w.pending = append(w.pending, []rune(content)...)
	_, _ = w.full.WriteString(content)
	if len(w.pending) >= deltaBatchRunes {
		return w.flush(ctx)
	}
	return nil
}

// addReasoning 累积模型思考增量，按与正文相同的批次发送事件。
func (w *deltaWriter) addReasoning(ctx context.Context, content string) error {
	w.reasoningPending = append(w.reasoningPending, []rune(content)...)
	_, _ = w.reasoning.WriteString(content)
	if len(w.reasoningPending) >= deltaBatchRunes {
		return w.flushReasoning(ctx)
	}
	return nil
}

// flush 先刷新思考事件，再更新消息行与正文事件，确保回放和实时订阅者
// 绝不会看到缺少已存储前缀的 delta。
func (w *deltaWriter) flush(ctx context.Context) error {
	if err := w.flushReasoning(ctx); err != nil {
		return err
	}
	if len(w.pending) == emptyPending {
		return nil
	}
	delta := logging.Redact(string(w.pending), w.apiKey)
	w.pending = w.pending[:emptyPending]
	content := logging.Redact(w.full.String(), w.apiKey)
	if err := w.messages.UpdateContent(ctx, w.messageID, content); err != nil {
		return err
	}
	_, err := w.events.Append(ctx, w.runID, "message.delta", map[string]any{
		"message_id": w.messageID.String(), "delta": delta,
	})
	if err != nil {
		return fmt.Errorf("append message delta: %w", err)
	}
	return nil
}

// flushReasoning 只追加思考事件；思考过程当前不落库，刷新后不再展示。
func (w *deltaWriter) flushReasoning(ctx context.Context) error {
	if len(w.reasoningPending) == emptyPending {
		return nil
	}
	delta := logging.Redact(string(w.reasoningPending), w.apiKey)
	w.reasoningPending = w.reasoningPending[:emptyPending]
	_, err := w.events.Append(ctx, w.runID, "message.reasoning", map[string]any{
		"message_id": w.messageID.String(), "delta": delta,
	})
	if err != nil {
		return fmt.Errorf("append message reasoning: %w", err)
	}
	return nil
}

// assistantSink 接收助手文本与思考增量；生产实现是 deltaWriter，
// 测试可注入内存实现。
type assistantSink interface {
	add(ctx context.Context, content string) error
	addReasoning(ctx context.Context, content string) error
	flush(ctx context.Context) error
}

// consumeFuture 遍历每一轮的模型 stream 并排空；助手文本已由工具调用检查器
// 在读取过程中写入 sink，这里只负责让流继续前进并传播错误。
func consumeFuture(
	future react.MessageFuture, writer *deltaWriter,
) error {
	streams := future.GetMessageStreams()
	for {
		stream, ok, err := streams.Next()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := consumeRound(stream, writer); err != nil {
			return err
		}
	}
}

func consumeRound(
	stream *schema.StreamReader[*schema.Message], writer *deltaWriter,
) error {
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if isContextError(err) {
				return err
			}
			return mapModelError(err, writer.apiKey)
		}
	}
}
