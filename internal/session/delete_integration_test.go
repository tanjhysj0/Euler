//go:build integration

package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"euler/internal/persistence"
)

// 回归：tool_calls.approval_id 与 approvals.tool_call_id 互相引用，
// 删除会话时必须先解除 tool_calls 一侧的外键，否则 approvals 删除会失败。
func TestDeleteSessionHandlesCircularApprovalReference(t *testing.T) {
	svc, db := newSessionServiceForTest(t)
	created, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)

	run := persistence.Run{
		ID: uuid.New(), SessionID: created.ID, Status: "succeeded", NextEventSeq: 1,
	}
	require.NoError(t, db.Create(&run).Error)

	toolCall := persistence.ToolCall{
		ID:            uuid.New(),
		RunID:         run.ID,
		Name:          "shell",
		ArgumentsJSON: datatypes.JSON([]byte(`{"command":"pwd"}`)),
		Status:        "succeeded",
		ResultJSON:    datatypes.JSON([]byte(`{"output":"/tmp"}`)),
	}
	require.NoError(t, db.Create(&toolCall).Error)

	approval := persistence.Approval{
		ID:                   uuid.New(),
		RunID:                run.ID,
		ToolCallID:           toolCall.ID,
		Status:               "approved",
		RequestedPayloadJSON: datatypes.JSON([]byte(`{}`)),
	}
	require.NoError(t, db.Create(&approval).Error)

	// 模拟审批通过后的关联：tool_calls.approval_id 指向 approvals.id。
	require.NoError(t, db.Model(&persistence.ToolCall{}).
		Where("id = ?", toolCall.ID).
		Update("approval_id", approval.ID).Error)

	message := persistence.Message{
		ID: uuid.New(), SessionID: created.ID, RunID: &run.ID,
		SessionSeq: 1, Role: "user", Content: "hello", Status: "completed",
	}
	require.NoError(t, db.Create(&message).Error)

	event := persistence.RunEvent{
		ID: uuid.New(), RunID: run.ID, Seq: 1, Type: "run.started",
		PayloadJSON: datatypes.JSON([]byte(`{}`)),
	}
	require.NoError(t, db.Create(&event).Error)

	require.NoError(t, svc.Delete(context.Background(), created.ID))

	checks := []struct {
		model any
		where string
		args  any
	}{
		{&persistence.Run{}, "id = ?", run.ID},
		{&persistence.ToolCall{}, "run_id = ?", run.ID},
		{&persistence.Approval{}, "run_id = ?", run.ID},
		{&persistence.RunEvent{}, "run_id = ?", run.ID},
		{&persistence.Message{}, "session_id = ?", created.ID},
	}
	for _, check := range checks {
		var count int64
		require.NoError(t, db.Model(check.model).Where(check.where, check.args).Count(&count).Error)
		require.Zero(t, count)
	}
}
