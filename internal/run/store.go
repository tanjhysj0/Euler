package run

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"euler/internal/event"
	"euler/internal/persistence"
)

// HealthChecker 报告后端数据库是否能响应请求。
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// Store 持久化 run、消息、审批以及工具状态迁移。
type Store struct {
	db      *gorm.DB
	events  *event.Store
	checker HealthChecker
}

// NewStore 构建一个通过 events 追加 run 事件的 Store。
func NewStore(db *gorm.DB, events *event.Store, checker HealthChecker) *Store {
	return &Store{db: db, events: events, checker: checker}
}

// Get 返回 run 及其工具调用、审批和最后的事件序号。
func (s *Store) Get(ctx context.Context, runID uuid.UUID) (Snapshot, error) {
	var row persistence.Run
	err := s.db.WithContext(ctx).First(&row, "id = ?", runID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("load run: %w", err)
	}
	toolCalls := make([]persistence.ToolCall, noCapacity)
	err = s.db.WithContext(ctx).Where("run_id = ?", runID).
		Order("created_at ASC, id ASC").Find(&toolCalls).Error
	if err != nil {
		return Snapshot{}, fmt.Errorf("load tool calls: %w", err)
	}
	approvals := make([]persistence.Approval, noCapacity)
	err = s.db.WithContext(ctx).Where("run_id = ?", runID).
		Order("requested_at ASC, id ASC").Find(&approvals).Error
	if err != nil {
		return Snapshot{}, fmt.Errorf("load approvals: %w", err)
	}
	return Snapshot{
		Run:          row,
		ToolCalls:    toolCalls,
		Approvals:    approvals,
		LastEventSeq: row.NextEventSeq - initialNextEventSeq,
	}, nil
}

// Exists 报告 runID 是否是一个已存在的 run。
func (s *Store) Exists(ctx context.Context, runID uuid.UUID) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&persistence.Run{}).
		Where("id = ?", runID).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("count run: %w", err)
	}
	return count > zeroCount, nil
}

// MarkRunning 将处于 queued 状态的 run 迁移到 running，并在同一事务中提交
// run.started 和 message.started 事件。若 run 已不处于 queued 状态则返回
// false，worker 便不会发布任何事件。
func (s *Store) MarkRunning(ctx context.Context, request Request) (bool, error) {
	var startedEvent, messageEvent event.Event
	started := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockRun(tx, request.RunID)
		if err != nil {
			return err
		}
		if !CanTransition(row.Status, StatusRunning) {
			return nil
		}
		updates := map[string]any{
			"status":     StatusRunning,
			"started_at": time.Now().UTC(),
		}
		if err = tx.Model(&persistence.Run{}).Where("id = ?", request.RunID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("start run: %w", err)
		}
		startedEvent, err = event.AppendTx(tx, request.RunID, eventRunStarted,
			map[string]any{
				"session_id":           request.SessionID,
				"assistant_message_id": request.AssistantMessageID,
			})
		if err != nil {
			return fmt.Errorf("append run.started: %w", err)
		}
		messageEvent, err = event.AppendTx(tx, request.RunID, eventMessageStarted,
			map[string]any{
				"message_id": request.AssistantMessageID,
				"role":       "assistant",
			})
		if err != nil {
			return fmt.Errorf("append message.started: %w", err)
		}
		started = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if started {
		s.events.Publish(startedEvent)
		s.events.Publish(messageEvent)
	}
	return started, nil
}

// StatusChange 描述非终态 run 状态之间一次受校验的状态迁移。
type StatusChange struct {
	RunID uuid.UUID
	From  string
	To    string
}

// SetStatusTx 在调用方持有的事务内将 run 在两个非终态状态之间迁移，并报告
// 该行是否发生变化。审批流程通过此函数将 run 状态、工具调用状态及其事件
// 绑定为一次原子写入，使调用方自己的事件成为该次迁移唯一可观察的记录。
// 先锁定 run 行可保持与 Finish 和 Cancel 一致的行锁顺序；context 随 tx 传递。
func SetStatusTx(tx *gorm.DB, change StatusChange) (bool, error) {
	row, err := lockRun(tx, change.RunID)
	if err != nil {
		return false, err
	}
	if row.Status == change.To {
		return false, nil
	}
	if row.Status != change.From || !CanTransition(row.Status, change.To) {
		return false,
			fmt.Errorf("%w: %s -> %s", ErrInvalidInput, row.Status, change.To)
	}
	err = tx.Model(&persistence.Run{}).Where("id = ?", change.RunID).
		Update("status", change.To).Error
	if err != nil {
		return false, fmt.Errorf("update run status: %w", err)
	}
	return true, nil
}

// FinishInput 描述一次终态迁移。
type FinishInput struct {
	RunID         uuid.UUID
	Status        string
	MessageStatus string
	ErrorCode     string
	ErrorMessage  string
	EventType     string
}

// Finish 应用一次终态迁移，并在同一事务中写入对应事件。重复进入相同的
// 终态是幂等的，不会再写入事件。
func (s *Store) Finish(ctx context.Context, input FinishInput) error {
	payload := map[string]any{}
	if input.ErrorCode != emptyText {
		payload["error_code"] = input.ErrorCode
	}
	if input.ErrorMessage != emptyText {
		payload["error_message"] = input.ErrorMessage
	}
	var stored event.Event
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockRun(tx, input.RunID)
		if err != nil {
			return err
		}
		if isTerminal(row.Status) {
			return nil
		}
		if !CanTransition(row.Status, input.Status) {
			return fmt.Errorf("%w: %s -> %s", ErrInvalidInput, row.Status, input.Status)
		}
		if err = applyTerminal(tx, input); err != nil {
			return err
		}
		stored, err = event.AppendTx(tx, input.RunID, input.EventType, payload)
		if err != nil {
			return fmt.Errorf("append terminal event: %w", err)
		}
		changed = true
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		s.events.Publish(stored)
	}
	return nil
}

func applyTerminal(tx *gorm.DB, input FinishInput) error {
	now := time.Now().UTC()
	updates := map[string]any{"status": input.Status, "finished_at": now}
	if input.ErrorCode != emptyText {
		updates["error_code"] = input.ErrorCode
	}
	if input.ErrorMessage != emptyText {
		updates["error_message"] = input.ErrorMessage
	}
	err := tx.Model(&persistence.Run{}).Where("id = ?", input.RunID).
		Updates(updates).Error
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	return finishRelated(tx, input.RunID, input.MessageStatus, now)
}

// Cancel 将活跃的 run 迁移为 cancelled，同时取消其流式消息、待处理审批和
// 活跃的工具调用。重复调用 Cancel 会返回最新快照，而不会再次发布
// run.cancelled 事件。
func (s *Store) Cancel(ctx context.Context, runID uuid.UUID) (Snapshot, error) {
	err := s.Finish(ctx, FinishInput{
		RunID:         runID,
		Status:        StatusCancelled,
		MessageStatus: messageCancelled,
		EventType:     eventRunCancelled,
	})
	if err != nil {
		return Snapshot{}, err
	}
	return s.Get(ctx, runID)
}

// Interrupt 绕过正常状态机，将一个活跃 run 修复为 interrupted。它仅用于
// 关闭和启动恢复流程。
func (s *Store) Interrupt(ctx context.Context, runID uuid.UUID) error {
	var stored event.Event
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := lockRun(tx, runID)
		if err != nil {
			return err
		}
		if isTerminal(row.Status) {
			return nil
		}
		now := time.Now().UTC()
		updates := map[string]any{"status": StatusInterrupted, "finished_at": now}
		if err = tx.Model(&persistence.Run{}).Where("id = ?", runID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("interrupt run: %w", err)
		}
		if err = finishRelated(tx, runID, messageInterrupted, now); err != nil {
			return err
		}
		stored, err = event.AppendTx(tx, runID, eventRunInterrupted, map[string]any{})
		if err != nil {
			return fmt.Errorf("append interrupt event: %w", err)
		}
		changed = true
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		s.events.Publish(stored)
	}
	return nil
}

// RecoverInterrupted 将所有处于 queued、running 或 waiting_approval 状态的
// run 修复为 interrupted。它在启动阶段、开始处理请求之前被调用一次。
func (s *Store) RecoverInterrupted(ctx context.Context) error {
	var runIDs []uuid.UUID
	err := s.db.WithContext(ctx).Model(&persistence.Run{}).
		Where("status IN ?", activeStatuses).Pluck("id", &runIDs).Error
	if err != nil {
		return fmt.Errorf("list active runs: %w", err)
	}
	for _, runID := range runIDs {
		if err = s.Interrupt(ctx, runID); err != nil {
			return fmt.Errorf("interrupt run %s: %w", runID, err)
		}
	}
	return nil
}

func lockRun(tx *gorm.DB, runID uuid.UUID) (persistence.Run, error) {
	var row persistence.Run
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&row, "id = ?", runID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return persistence.Run{}, ErrNotFound
	}
	if err != nil {
		return persistence.Run{}, fmt.Errorf("lock run: %w", err)
	}
	return row, nil
}

func finishRelated(tx *gorm.DB, runID uuid.UUID, messageStatus string, now time.Time) error {
	message := tx.Model(&persistence.Message{}).
		Where("run_id = ? AND role = ? AND status = ?", runID, "assistant", messageStreaming).
		Updates(map[string]any{"status": messageStatus, "completed_at": now})
	if message.Error != nil {
		return fmt.Errorf("finish message: %w", message.Error)
	}
	approval := tx.Model(&persistence.Approval{}).
		Where("run_id = ? AND status = ?", runID, "pending").
		Updates(map[string]any{"status": "cancelled", "decided_at": now})
	if approval.Error != nil {
		return fmt.Errorf("cancel approvals: %w", approval.Error)
	}
	toolCall := tx.Model(&persistence.ToolCall{}).
		Where("run_id = ? AND status IN ?", runID,
			[]string{"pending", "waiting_approval", "running"}).
		Updates(map[string]any{"status": "cancelled", "finished_at": now})
	if toolCall.Error != nil {
		return fmt.Errorf("cancel tool calls: %w", toolCall.Error)
	}
	return nil
}
