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

// HealthChecker reports whether the backing database answers requests.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// Store persists runs, messages, approvals and tool state transitions.
type Store struct {
	db      *gorm.DB
	events  *event.Store
	checker HealthChecker
}

// NewStore builds a Store that appends run events through events.
func NewStore(db *gorm.DB, events *event.Store, checker HealthChecker) *Store {
	return &Store{db: db, events: events, checker: checker}
}

// Get returns the run together with its tool calls, approvals and last event
// sequence.
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

// Exists reports whether runID is a known run.
func (s *Store) Exists(ctx context.Context, runID uuid.UUID) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&persistence.Run{}).
		Where("id = ?", runID).Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("count run: %w", err)
	}
	return count > zeroCount, nil
}

// MarkRunning moves a queued run to running and commits run.started and
// message.started in the same transaction. A run that is no longer queued
// reports false so the worker publishes nothing.
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

// StatusChange names one guarded transition between non-terminal run statuses.
type StatusChange struct {
	RunID uuid.UUID
	From  string
	To    string
}

// SetStatusTx moves a run between two non-terminal statuses inside a
// transaction the caller owns and reports whether the row changed. Approval
// flows bind the run status, the tool call status and their event into one
// atomic write through this function, so the caller's own event stays the
// single observable record of the transition. Locking the run first keeps the
// row lock order identical to Finish and Cancel; the context travels with tx.
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

// FinishInput describes one terminal transition.
type FinishInput struct {
	RunID         uuid.UUID
	Status        string
	MessageStatus string
	ErrorCode     string
	EventType     string
}

// Finish applies a terminal transition and writes its event in the same
// transaction. Repeating the same terminal state is idempotent and does not
// write an event.
func (s *Store) Finish(ctx context.Context, input FinishInput) error {
	payload := map[string]any{}
	if input.ErrorCode != "" {
		payload["error_code"] = input.ErrorCode
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
	if input.ErrorCode != "" {
		updates["error_code"] = input.ErrorCode
	}
	err := tx.Model(&persistence.Run{}).Where("id = ?", input.RunID).
		Updates(updates).Error
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	return finishRelated(tx, input.RunID, input.MessageStatus, now)
}

// Cancel moves an active run to cancelled, cancels its streaming message,
// pending approvals and active tool calls. Repeating Cancel returns the latest
// snapshot without publishing a second run.cancelled event.
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

// Interrupt repairs one active run as interrupted without using the normal
// state machine. It is reserved for shutdown and startup recovery.
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

// RecoverInterrupted repairs every queued, running or waiting_approval run as
// interrupted. It is called once during startup, before serving requests.
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
