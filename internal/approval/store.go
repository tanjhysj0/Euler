package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"euler/internal/event"
	"euler/internal/persistence"
	"euler/internal/run"
)

const (
	// toolPending and toolRunning are the only tool call states an approval may
	// take over. The parked and terminal states reuse the run status machine
	// and the approval status constants, which spell the same strings.
	toolPending = "pending"
	toolRunning = "running"

	// columnStatus is the shared status column of approvals and tool calls.
	columnStatus = "status"
	// columnApprovalID links a parked tool call to its approval row.
	columnApprovalID = "approval_id"
	// queryByID selects one row by primary key.
	queryByID = "id = ?"

	// toolRejectedQuery parks the tool call of an approval that is being
	// resolved; requiring the parked state keeps a cancelled run untouched.
	toolParkedQuery = "id = ? AND status = ?"

	// pendingLeftQuery counts the approvals of one run that still park it.
	pendingLeftQuery = "run_id = ? AND status = ? AND id <> ?"
	// nonePending is the count that lets a parked run resume.
	nonePending = 0
	// decisionAbsent marks an approval row without a decision payload.
	decisionAbsent = 0
)

// DBStore persists approvals in PostgreSQL together with the tool call and run
// statuses they own. State and event share one transaction so a committed
// decision is always replayable; callers publish only after the commit.
type DBStore struct{ db *gorm.DB }

// NewDBStore wires the durable approval store.
func NewDBStore(db *gorm.DB) *DBStore { return &DBStore{db: db} }

var _ Store = (*DBStore)(nil)

// Open writes the pending approval, parks the tool call and the run in
// waiting_approval and appends approval.requested, all in one transaction.
func (s *DBStore) Open(ctx context.Context, request Request) (event.Event, error) {
	var appended event.Event
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		parked, err := run.SetStatusTx(tx,
			runStatusChange(request.RunID, run.StatusRunning, run.StatusWaitingApproval))
		if errors.Is(err, run.ErrInvalidInput) {
			return fmt.Errorf("%w: run %s is not running", ErrInvalidRequest, request.RunID)
		}
		if err != nil {
			return err
		}
		if !parked {
			return fmt.Errorf("%w: run %s is not running", ErrInvalidRequest, request.RunID)
		}
		call, err := lockToolCall(tx, request)
		if err != nil {
			return err
		}
		// The approval row must exist before the tool call references it:
		// tool_calls.approval_id is an immediate foreign key.
		row, err := newApprovalRow(request)
		if err != nil {
			return err
		}
		if err = tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create approval: %w", err)
		}
		if err = tx.Model(&call).Updates(map[string]any{
			columnStatus:     run.StatusWaitingApproval,
			columnApprovalID: request.ID,
		}).Error; err != nil {
			return fmt.Errorf("park tool call: %w", err)
		}
		appended, err = event.AppendTx(tx,
			request.RunID, EventRequested, requestedPayload(request))
		return err
	})
	if err != nil {
		return event.Event{}, err
	}
	return appended, nil
}

// Resolve records the first decision for id, resumes the run when no approval
// still parks it and appends approval.resolved. A row that is no longer pending
// returns ErrAlreadyDecided with the durable decision it already holds.
func (s *DBStore) Resolve(
	ctx context.Context,
	id uuid.UUID,
	resolution Resolution,
) (Record, event.Event, error) {
	var (
		record   Record
		appended event.Event
	)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if lockErr := lockRunForApproval(tx, id); lockErr != nil {
			return lockErr
		}
		row, err := lockApproval(tx, id)
		if err != nil {
			return err
		}
		if row.Status != StatusPending {
			record = newRecord(row)
			return ErrAlreadyDecided
		}
		now := time.Now().UTC()
		if err = writeDecision(tx, &row, resolution, now); err != nil {
			return err
		}
		if err = resolveToolCall(tx, row.ToolCallID, resolution, now); err != nil {
			return err
		}
		if err = releaseRun(tx, row); err != nil {
			return err
		}
		record = newRecord(row)
		appended, err = event.AppendTx(tx, row.RunID,
			EventResolved, resolvedPayload(id, row.ToolCallID, resolution))
		return err
	})
	if err != nil {
		return record, event.Event{}, err
	}
	return record, appended, nil
}

// Get loads one approval.
func (s *DBStore) Get(ctx context.Context, id uuid.UUID) (Record, error) {
	var row persistence.Approval
	err := s.db.WithContext(ctx).First(&row, queryByID, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("load approval: %w", err)
	}
	return newRecord(row), nil
}

// lockToolCall checks that the tool call belongs to the run, is still waiting
// to start and has no approval of its own, holding its row lock until the
// surrounding transaction ends.
func lockToolCall(tx *gorm.DB, request Request) (persistence.ToolCall, error) {
	var call persistence.ToolCall
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&call, "id = ? AND run_id = ?", request.ToolCallID, request.RunID).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return persistence.ToolCall{},
			fmt.Errorf("%w: tool call %s", ErrInvalidRequest, request.ToolCallID)
	case err != nil:
		return persistence.ToolCall{}, fmt.Errorf("lock tool call: %w", err)
	case call.Status != toolPending && call.Status != toolRunning:
		return persistence.ToolCall{},
			fmt.Errorf("%w: tool call status %q", ErrInvalidRequest, call.Status)
	case call.ApprovalID != nil:
		return persistence.ToolCall{},
			fmt.Errorf("%w: tool call already has an approval", ErrInvalidRequest)
	default:
		return call, nil
	}
}

// newApprovalRow marshals the reviewed request into a pending approval row.
func newApprovalRow(request Request) (persistence.Approval, error) {
	payload, err := json.Marshal(requestedPayload(request))
	if err != nil {
		return persistence.Approval{}, fmt.Errorf("encode approval request: %w", err)
	}
	return persistence.Approval{
		ID:                   request.ID,
		RunID:                request.RunID,
		ToolCallID:           request.ToolCallID,
		Status:               StatusPending,
		RequestedPayloadJSON: datatypes.JSON(payload),
	}, nil
}

// lockRunForApproval reads the approval's run without locking, then locks the
// run row before the approval itself. Every transaction that touches a run
// and its approvals must lock in the order run → approval → tool call:
// run.Finish/Cancel lock the run first, so locking the approval first here
// would invert the order and can deadlock (PostgreSQL 40P01).
func lockRunForApproval(tx *gorm.DB, id uuid.UUID) error {
	var approval persistence.Approval
	err := tx.Select("run_id").First(&approval, queryByID, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read approval run: %w", err)
	}
	var runRow persistence.Run
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&runRow, queryByID, approval.RunID).Error
	if err != nil {
		return fmt.Errorf("lock run for approval: %w", err)
	}
	return nil
}

// lockApproval loads one approval with a row lock.
func lockApproval(tx *gorm.DB, id uuid.UUID) (persistence.Approval, error) {
	var row persistence.Approval
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, queryByID, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return persistence.Approval{}, ErrNotFound
	}
	if err != nil {
		return persistence.Approval{}, fmt.Errorf("lock approval: %w", err)
	}
	return row, nil
}

// writeDecision stores the decision and its outcome payload of one approval,
// then mirrors the committed columns onto row so the caller's record matches
// the database without a second read.
func writeDecision(
	tx *gorm.DB,
	row *persistence.Approval,
	resolution Resolution,
	now time.Time,
) error {
	decision, err := json.Marshal(map[string]any{
		keyApproved: resolution.Approved,
		keyReason:   resolution.Reason,
	})
	if err != nil {
		return fmt.Errorf("encode approval decision: %w", err)
	}
	updates := map[string]any{
		columnStatus:            resolution.Status,
		"decided_at":            now,
		"decision_payload_json": datatypes.JSON(decision),
	}
	err = tx.Model(&persistence.Approval{}).Where(queryByID, row.ID).Updates(updates).Error
	if err != nil {
		return fmt.Errorf("decide approval: %w", err)
	}
	row.Status, row.DecidedAt = resolution.Status, &now
	row.DecisionPayloadJSON = datatypes.JSON(decision)
	return nil
}

// resolveToolCall moves the parked tool call on: an approved call runs, a
// rejected or cancelled one is finished. Requiring the parked state keeps
// cancellations written by the run state machine untouched.
func resolveToolCall(
	tx *gorm.DB,
	toolCallID uuid.UUID,
	resolution Resolution,
	now time.Time,
) error {
	updates := map[string]any{columnStatus: toolRunning}
	if !resolution.Approved {
		updates[columnStatus] = resolution.Status
		updates["finished_at"] = now
	}
	err := tx.Model(&persistence.ToolCall{}).
		Where(toolParkedQuery, toolCallID, run.StatusWaitingApproval).
		Updates(updates).Error
	if err != nil {
		return fmt.Errorf("resolve tool call: %w", err)
	}
	return nil
}

// releaseRun resumes a parked run once no approval still holds it. A run that
// cancellation already closed is left untouched by SetStatusTx.
func releaseRun(tx *gorm.DB, row persistence.Approval) error {
	pending, err := pendingApprovals(tx, row)
	if err != nil {
		return err
	}
	if pending > nonePending {
		return nil
	}
	_, err = run.SetStatusTx(tx,
		runStatusChange(row.RunID, run.StatusWaitingApproval, run.StatusRunning))
	return err
}

// pendingApprovals counts the approvals of one run still waiting for a
// decision, excluding the row being resolved.
func pendingApprovals(tx *gorm.DB, row persistence.Approval) (int64, error) {
	var count int64
	err := tx.Model(&persistence.Approval{}).
		Where(pendingLeftQuery, row.RunID, StatusPending, row.ID).Count(&count).Error
	if err != nil {
		return nonePending, fmt.Errorf("count pending approvals: %w", err)
	}
	return count, nil
}

// newRecord projects one approval row onto the API view. A stored decision
// payload wins over the status column so a rejected row never reads as approved.
func newRecord(row persistence.Approval) Record {
	decision := Decision{Status: row.Status, Approved: row.Status == StatusApproved}
	if len(row.DecisionPayloadJSON) > decisionAbsent {
		var stored struct {
			Approved bool   `json:"approved"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal(row.DecisionPayloadJSON, &stored) == nil {
			decision.Approved, decision.Reason = stored.Approved, stored.Reason
		}
	}
	return Record{
		ID:          row.ID,
		RunID:       row.RunID,
		ToolCallID:  row.ToolCallID,
		Status:      row.Status,
		RequestedAt: row.RequestedAt,
		DecidedAt:   row.DecidedAt,
		Decision:    decision,
	}
}
