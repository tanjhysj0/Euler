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
	// toolPending 和 toolRunning 是审批可以接管的仅有的工具调用状态。
	// 停驻态和终态复用运行状态机与审批状态常量，它们的字符串拼写相同。
	toolPending = "pending"
	toolRunning = "running"

	// columnStatus 是审批与工具调用共用的状态列。
	columnStatus = "status"
	// columnApprovalID 将停驻的工具调用关联到其审批行。
	columnApprovalID = "approval_id"
	// queryByID 按主键选取一行。
	queryByID = "id = ?"

	// toolRejectedQuery 用于更新正在被决策的审批所停驻的工具调用；
	// 要求处于停驻状态可使已取消的运行保持不动。
	toolParkedQuery = "id = ? AND status = ?"

	// pendingLeftQuery 统计一个运行中仍使其停驻的审批数量。
	pendingLeftQuery = "run_id = ? AND status = ? AND id <> ?"
	// nonePending 是允许停驻运行恢复的计数值。
	nonePending = 0
	// decisionAbsent 表示审批行没有决策载荷。
	decisionAbsent = 0
)

// DBStore 将审批及其所辖的工具调用与运行状态持久化到 PostgreSQL。
// 状态与事件共享同一个事务，因此已提交的决策始终可回放；
// 调用方只在提交后才发布事件。
type DBStore struct{ db *gorm.DB }

// NewDBStore 组装持久化审批存储。
func NewDBStore(db *gorm.DB) *DBStore { return &DBStore{db: db} }

var _ Store = (*DBStore)(nil)

// Open 写入待决策的审批，把工具调用和运行停在 waiting_approval 状态，
// 并追加 approval.requested，全部在同一个事务中完成。
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
		// 审批行必须先于工具调用对它的引用而存在：
		// tool_calls.approval_id 是立即校验的外键。
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

// Resolve 记录 id 的首次决策，当不再有审批使运行停驻时恢复运行，
// 并追加 approval.resolved。已不处于 pending 的行返回 ErrAlreadyDecided，
// 并附带其已持久化的决策。
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

// Get 加载一个审批。
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

// lockToolCall 检查工具调用属于该运行、仍在等待启动且尚无自己的审批，
// 并持有其行锁直到外层事务结束。
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

// newApprovalRow 将待审核的请求序列化为一行 pending 审批。
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

// lockRunForApproval 先不加锁地读取审批所属的运行，再在锁定审批本身
// 之前锁定运行行。所有涉及运行及其审批的事务必须按
// run → approval → tool call 的顺序加锁：run.Finish/Cancel 先锁运行，
// 若这里先锁审批就会颠倒顺序，可能导致死锁（PostgreSQL 40P01）。
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

// lockApproval 以行锁加载一个审批。
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

// writeDecision 存储一个审批的决策及其结果载荷，然后把已提交的列
// 同步回 row，使调用方的记录无需二次读取即与数据库一致。
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

// resolveToolCall 推进停驻的工具调用：批准的调用转为运行，
// 被拒绝或取消的调用则结束。要求处于停驻状态可使运行状态机
// 写入的取消结果保持不动。
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

// releaseRun 在不再有审批扣住运行时恢复停驻的运行。
// 已被取消关闭的运行不会被 SetStatusTx 改动。
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

// pendingApprovals 统计一个运行中仍在等待决策的审批数量，
// 不含正在被决策的那一行。
func pendingApprovals(tx *gorm.DB, row persistence.Approval) (int64, error) {
	var count int64
	err := tx.Model(&persistence.Approval{}).
		Where(pendingLeftQuery, row.RunID, StatusPending, row.ID).Count(&count).Error
	if err != nil {
		return nonePending, fmt.Errorf("count pending approvals: %w", err)
	}
	return count, nil
}

// newRecord 将一行审批映射为 API 视图。已存储的决策载荷优先于状态列，
// 因此被拒绝的行绝不会被读成已批准。
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
