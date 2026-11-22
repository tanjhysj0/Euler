package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"euler/internal/event"
	"euler/internal/logging"
	"euler/internal/persistence"
	"euler/internal/run"
)

// deltaBatchRunes：累计达到这么多 UTF-8 rune，或当前模型 stream 结束时，
// 就 flush 一次助手文本。
const (
	deltaBatchRunes = 128
	emptyPending    = 0
	emptyContent    = ""
	emptyParts      = 0
	lastPartOffset  = 1
)

// deltaWriter 将助手文本批量写入流式消息行和 message.delta 事件。
// 同时维护一份有序的文本、思考与工具片段，随每次落盘整体写入 messages.parts。
// 正文检查器与工具执行可能来自不同 goroutine，用互斥锁保护片段状态。
type deltaWriter struct {
	mu               sync.Mutex
	messages         MessageRecorder
	events           event.Publisher
	runID            uuid.UUID
	messageID        uuid.UUID
	apiKey           string
	full             strings.Builder
	pending          []rune
	reasoning        strings.Builder
	reasoningPending []rune
	parts            []persistence.Part
}

func newDeltaWriter(
	events event.Publisher, request run.Request, messages MessageRecorder, apiKey string,
) *deltaWriter {
	return &deltaWriter{
		messages: messages, events: events, runID: request.RunID,
		messageID: request.AssistantMessageID, apiKey: apiKey,
		parts: make([]persistence.Part, emptyParts),
	}
}

// text 返回已累积的助手全文；只应在 stream 全部结束后读取。
func (w *deltaWriter) text() string {
	return w.full.String()
}

func (w *deltaWriter) add(ctx context.Context, content string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, []rune(content)...)
	w.appendPart(persistence.PartTypeText, content)
	_, _ = w.full.WriteString(content)
	if len(w.pending) >= deltaBatchRunes {
		return w.flushLocked(ctx)
	}
	return nil
}

// addReasoning 累积模型思考增量，按与正文相同的批次发送事件。
func (w *deltaWriter) addReasoning(ctx context.Context, content string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reasoningPending = append(w.reasoningPending, []rune(content)...)
	w.appendPart(persistence.PartTypeReasoning, content)
	_, _ = w.reasoning.WriteString(content)
	if len(w.reasoningPending) >= deltaBatchRunes {
		return w.flushReasoningLocked(ctx)
	}
	return nil
}

// flush 先刷新思考事件，再更新消息行与正文事件，确保回放和实时订阅者
// 绝不会看到缺少已存储前缀的 delta。
func (w *deltaWriter) flush(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked(ctx)
}

func (w *deltaWriter) flushLocked(ctx context.Context) error {
	if err := w.flushReasoningLocked(ctx); err != nil {
		return err
	}
	if len(w.pending) == emptyPending {
		return nil
	}
	delta := logging.Redact(string(w.pending), w.apiKey)
	w.pending = w.pending[:emptyPending]
	if err := w.saveLocked(ctx); err != nil {
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

// flushReasoningLocked 只追加思考事件；思考片段随正文一起落盘。
func (w *deltaWriter) flushReasoningLocked(ctx context.Context) error {
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

// appendPart 把同一类型的连续增量合并进末尾片段，保证文本与思考各成一段。
func (w *deltaWriter) appendPart(partType, content string) {
	last := len(w.parts) - lastPartOffset
	if last >= emptyParts && w.parts[last].Type == partType {
		w.parts[last].Text += content
		return
	}
	w.parts = append(w.parts, persistence.Part{Type: partType, Text: content})
}

// AppendToolPart 在助手片段末尾追加一次工具调用并立即落盘。工具执行与
// 正文检查器可能并发，因此在锁内保存完整片段，避免覆盖已经写入的文本。
func (w *deltaWriter) AppendToolPart(ctx context.Context, toolCallID uuid.UUID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.parts = append(w.parts, persistence.Part{
		Type: persistence.PartTypeTool, ToolCallID: toolCallID.String(),
	})
	return w.saveLocked(ctx)
}

// saveLocked 把当前正文与全部片段写入消息行；片段中的文本按密钥脱敏。
func (w *deltaWriter) saveLocked(ctx context.Context) error {
	parts := make([]persistence.Part, len(w.parts))
	for index, part := range w.parts {
		if part.Type != persistence.PartTypeTool {
			part.Text = logging.Redact(part.Text, w.apiKey)
		}
		parts[index] = part
	}
	content := logging.Redact(w.full.String(), w.apiKey)
	return w.messages.UpdateMessage(ctx, w.messageID, content, parts)
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
