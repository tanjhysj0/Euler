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
// 只有 future 消费者 goroutine 会访问它。
type deltaWriter struct {
	messages  MessageRecorder
	events    event.Publisher
	runID     uuid.UUID
	messageID uuid.UUID
	apiKey    string
	full      strings.Builder
	pending   []rune
}

func newDeltaWriter(
	events event.Publisher, request run.Request, messages MessageRecorder, apiKey string,
) *deltaWriter {
	return &deltaWriter{
		messages: messages, events: events, runID: request.RunID,
		messageID: request.AssistantMessageID, apiKey: apiKey,
	}
}

func (w *deltaWriter) add(ctx context.Context, content string) error {
	w.pending = append(w.pending, []rune(content)...)
	_, _ = w.full.WriteString(content)
	if len(w.pending) >= deltaBatchRunes {
		return w.flush(ctx)
	}
	return nil
}

// flush 先更新消息行，再追加事件，确保回放和实时订阅者绝不会看到
// 缺少已存储前缀的 delta。
func (w *deltaWriter) flush(ctx context.Context) error {
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

// consumeFuture 遍历每一轮的模型 stream；助手文本增量写入，
// 而 tool 角色的消息只消费不展示。
func consumeFuture(
	ctx context.Context, future react.MessageFuture, writer *deltaWriter,
) error {
	streams := future.GetMessageStreams()
	for {
		stream, ok, err := streams.Next()
		if err != nil {
			return err
		}
		if !ok {
			return writer.flush(ctx)
		}
		if err := consumeRound(ctx, stream, writer); err != nil {
			return err
		}
	}
}

func consumeRound(
	ctx context.Context, stream *schema.StreamReader[*schema.Message], writer *deltaWriter,
) error {
	defer stream.Close()
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return writer.flush(ctx)
		}
		if err != nil {
			if isContextError(err) {
				return err
			}
			return mapModelError(err, writer.apiKey)
		}
		if message.Role == schema.Assistant && message.Content != emptyContent {
			if err := writer.add(ctx, message.Content); err != nil {
				return err
			}
		}
	}
}
