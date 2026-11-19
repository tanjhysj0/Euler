package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

// gormRecorder 是 CallRecorder 的数据库实现。
type gormRecorder struct {
	db *gorm.DB
}

// NewGormRecorder 构建基于 GORM 的工具调用记录器。
func NewGormRecorder(db *gorm.DB) CallRecorder { return gormRecorder{db: db} }

// Start 写入一条 pending 状态的工具调用。
func (r gormRecorder) Start(ctx context.Context, call Call) error {
	now := time.Now().UTC()
	row := persistence.ToolCall{
		ID:            call.ID,
		RunID:         call.RunID,
		Name:          call.Name,
		ArgumentsJSON: datatypes.JSON(call.ArgumentsJSON),
		Status:        toolStatusPending,
		CreatedAt:     now,
		StartedAt:     &now,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create tool call: %w", err)
	}
	return nil
}

// Succeed 记录成功状态与结果。
func (r gormRecorder) Succeed(ctx context.Context, id uuid.UUID, resultJSON string) error {
	return r.update(ctx, id, map[string]any{
		"status":      toolStatusSucceeded,
		"result_json": datatypes.JSON(resultJSON),
		"finished_at": time.Now().UTC(),
	})
}

// Fail 记录失败状态；resultJSON 与 message 按非空字段写入。
func (r gormRecorder) Fail(
	ctx context.Context, id uuid.UUID, resultJSON, message string,
) error {
	updates := map[string]any{
		"status":      toolStatusFailed,
		"finished_at": time.Now().UTC(),
	}
	if resultJSON != "" {
		updates["result_json"] = datatypes.JSON(resultJSON)
	}
	if message != "" {
		updates["error_message"] = message
	}
	return r.update(ctx, id, updates)
}

// Reject 只写入拒绝结果；状态与完成时间由审批服务负责。
func (r gormRecorder) Reject(ctx context.Context, id uuid.UUID, resultJSON string) error {
	return r.update(ctx, id, map[string]any{
		"result_json": datatypes.JSON(resultJSON),
	})
}

func (r gormRecorder) update(ctx context.Context, id uuid.UUID, updates map[string]any) error {
	err := r.db.WithContext(ctx).Model(&persistence.ToolCall{}).
		Where("id = ?", id).Updates(updates).Error
	if err != nil {
		return fmt.Errorf("update tool call: %w", err)
	}
	return nil
}
