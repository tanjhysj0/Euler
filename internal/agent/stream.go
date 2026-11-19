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
	"gorm.io/gorm"

	"euler/internal/event"
	"euler/internal/logging"
	"euler/internal/persistence"
	"euler/internal/run"
)

// deltaBatchRunes flushes assistant text once this many UTF-8 runes have
// accumulated, or when the current model stream ends.
const (
	deltaBatchRunes = 128
	emptyPending    = 0
	emptyContent    = ""
)

// deltaWriter batches assistant text into the streaming message row and
// message.delta events. Only the future consumer goroutine touches it.
type deltaWriter struct {
	db        *gorm.DB
	events    event.Publisher
	runID     uuid.UUID
	messageID uuid.UUID
	apiKey    string
	full      strings.Builder
	pending   []rune
}

func newDeltaWriter(deps Deps, request run.Request, apiKey string) *deltaWriter {
	return &deltaWriter{
		db: deps.DB, events: deps.Events, runID: request.RunID,
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

// flush updates the message row first, then appends the event, so replay and
// live subscribers never see a delta without its stored prefix.
func (w *deltaWriter) flush(ctx context.Context) error {
	if len(w.pending) == emptyPending {
		return nil
	}
	delta := logging.Redact(string(w.pending), w.apiKey)
	w.pending = w.pending[:emptyPending]
	content := logging.Redact(w.full.String(), w.apiKey)
	err := w.db.WithContext(ctx).Model(&persistence.Message{}).
		Where("id = ?", w.messageID).Update("content", content).Error
	if err != nil {
		return fmt.Errorf("update message content: %w", err)
	}
	_, err = w.events.Append(ctx, w.runID, "message.delta", map[string]any{
		"message_id": w.messageID.String(), "delta": delta,
	})
	if err != nil {
		return fmt.Errorf("append message delta: %w", err)
	}
	return nil
}

// consumeFuture walks every per-round model stream; assistant text is written
// incrementally while tool role messages are drained without display.
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
