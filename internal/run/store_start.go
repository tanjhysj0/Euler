package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"euler/internal/persistence"
)

// Start 在一个事务中创建一个 queued 状态的 run 及其用户消息和流式 assistant
// 消息。它返回工作区并发警告，便于调用方提示同一目录下其他会话的并发编辑。
func (s *Store) Start(
	ctx context.Context, sessionID uuid.UUID, content string,
) (StartResult, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return StartResult{}, ErrInvalidInput
	}
	if s.checker != nil {
		if err := s.checker.Ping(ctx); err != nil {
			return StartResult{}, fmt.Errorf("database unavailable: %w", err)
		}
	}
	var result StartResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return startTx(tx, sessionID, trimmed, &result)
	})
	if errors.Is(err, errActiveRunConflict) {
		return StartResult{}, ErrActiveRun
	}
	if err != nil {
		return StartResult{}, err
	}
	return result, nil
}

func startTx(tx *gorm.DB, sessionID uuid.UUID, content string, result *StartResult) error {
	var session persistence.Session
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&session, "id = ?", sessionID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock session: %w", err)
	}
	if err = ensureNoActiveRun(tx, sessionID); err != nil {
		return err
	}
	now := time.Now().UTC()
	rows, err := insertRunAndMessages(tx, sessionID, content, now)
	if err != nil {
		return err
	}
	updates := map[string]any{"last_active_at": now, "updated_at": now}
	err = tx.Model(&persistence.Session{}).Where("id = ?", sessionID).Updates(updates).Error
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	warning, err := workspaceWarning(tx, session.WorkspaceID, sessionID)
	if err != nil {
		return err
	}
	*result = StartResult{
		Snapshot: Snapshot{
			Run:          rows.run,
			ToolCalls:    make([]persistence.ToolCall, noCapacity),
			Approvals:    make([]persistence.Approval, noCapacity),
			LastEventSeq: emptyLastEventSeq,
		},
		UserMessage:      rows.user,
		AssistantMessage: rows.assistant,
		WorkspaceWarning: warning,
		WorkspaceID:      session.WorkspaceID,
	}
	return nil
}

type startedRows struct {
	run       persistence.Run
	user      persistence.Message
	assistant persistence.Message
}

func insertRunAndMessages(
	tx *gorm.DB, sessionID uuid.UUID, content string, now time.Time,
) (startedRows, error) {
	seq, err := nextSessionSeq(tx, sessionID)
	if err != nil {
		return startedRows{}, err
	}
	runID := uuid.New()
	row := persistence.Run{
		ID:           runID,
		SessionID:    sessionID,
		Status:       StatusQueued,
		NextEventSeq: initialNextEventSeq,
		CreatedAt:    now,
	}
	if err = tx.Create(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return startedRows{}, errActiveRunConflict
		}
		return startedRows{}, err
	}
	user := persistence.Message{
		ID: uuid.New(), SessionID: sessionID, RunID: &runID, SessionSeq: seq,
		Role: "user", Content: content, Status: messageCompleted,
		Parts:     persistence.MarshalParts(persistence.TextParts(content)),
		CreatedAt: now, CompletedAt: &now,
	}
	assistant := persistence.Message{
		ID: uuid.New(), SessionID: sessionID, RunID: &runID,
		SessionSeq: seq + sessionSeqStep, Role: "assistant",
		Parts:  persistence.MarshalParts(nil),
		Status: messageStreaming, CreatedAt: now,
	}
	if err = tx.Create(&[]persistence.Message{user, assistant}).Error; err != nil {
		return startedRows{}, fmt.Errorf("insert messages: %w", err)
	}
	return startedRows{run: row, user: user, assistant: assistant}, nil
}

func ensureNoActiveRun(tx *gorm.DB, sessionID uuid.UUID) error {
	var count int64
	err := tx.Model(&persistence.Run{}).
		Where("session_id = ? AND status IN ?", sessionID, activeStatuses).
		Count(&count).Error
	if err != nil {
		return fmt.Errorf("check active runs: %w", err)
	}
	if count > zeroCount {
		return ErrActiveRun
	}
	return nil
}

func nextSessionSeq(tx *gorm.DB, sessionID uuid.UUID) (int64, error) {
	var maxSeq int64
	row := tx.Model(&persistence.Message{}).
		Select("COALESCE(MAX(session_seq), 0)").
		Where("session_id = ?", sessionID).Row()
	if err := row.Scan(&maxSeq); err != nil {
		return noSequence, fmt.Errorf("next session sequence: %w", err)
	}
	return maxSeq + sessionSeqStep, nil
}

func workspaceWarning(tx *gorm.DB, workspaceID, sessionID uuid.UUID) (bool, error) {
	var count int64
	err := tx.Model(&persistence.Run{}).
		Joins("JOIN sessions ON sessions.id = runs.session_id").
		Where("sessions.workspace_id = ?", workspaceID).
		Where("sessions.id <> ?", sessionID).
		Where("runs.status IN ?", activeStatuses).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check workspace runs: %w", err)
	}
	return count > zeroCount, nil
}
