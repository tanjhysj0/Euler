//go:build integration

package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

type failingChecker struct{}

func (failingChecker) Ping(context.Context) error {
	return errors.New("database down")
}

func TestStoreStartCreatesRunAndMessages(t *testing.T) {
	fixture := newManagerFixture(t)
	result, err := fixture.store.Start(context.Background(), fixture.sessionID, "hello")
	require.NoError(t, err)
	require.Equal(t, StatusQueued, result.Run.Status)
	require.Equal(t, int64(1), result.Run.NextEventSeq)
	require.Zero(t, result.LastEventSeq)
	require.False(t, result.WorkspaceWarning)
	require.NotEqual(t, uuid.Nil, result.WorkspaceID)
	require.Equal(t, "user", result.UserMessage.Role)
	require.Equal(t, "completed", result.UserMessage.Status)
	require.Equal(t, "hello", result.UserMessage.Content)
	require.Equal(t, int64(1), result.UserMessage.SessionSeq)
	require.Equal(t, "assistant", result.AssistantMessage.Role)
	require.Equal(t, messageStreaming, result.AssistantMessage.Status)
	require.Equal(t, int64(2), result.AssistantMessage.SessionSeq)
	require.NotNil(t, result.AssistantMessage.RunID)
	require.Equal(t, result.Run.ID, *result.AssistantMessage.RunID)
}

func TestStoreStartRejectsBlankContent(t *testing.T) {
	fixture := newManagerFixture(t)
	_, err := fixture.store.Start(context.Background(), fixture.sessionID, "   ")
	require.ErrorIs(t, err, ErrInvalidInput)
	var runs int64
	require.NoError(t, fixture.db.Model(&persistence.Run{}).
		Where("session_id = ?", fixture.sessionID).Count(&runs).Error)
	require.Zero(t, runs)
	var messages int64
	require.NoError(t, fixture.db.Model(&persistence.Message{}).
		Where("session_id = ?", fixture.sessionID).Count(&messages).Error)
	require.Zero(t, messages)
}

func TestStoreStartUnknownSession(t *testing.T) {
	fixture := newManagerFixture(t)
	_, err := fixture.store.Start(context.Background(), uuid.New(), "hello")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStoreStartWarnsOnSharedWorkspace(t *testing.T) {
	fixture := newManagerFixture(t)
	var current persistence.Session
	require.NoError(t, fixture.db.First(&current, "id = ?", fixture.sessionID).Error)
	other := persistence.Session{
		ID: uuid.New(), WorkspaceID: current.WorkspaceID, ModelProfileID: current.ModelProfileID,
		Title: "shared", Status: "active",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), LastActiveAt: time.Now().UTC(),
	}
	require.NoError(t, fixture.db.Create(&other).Error)
	_, err := fixture.store.Start(context.Background(), fixture.sessionID, "first")
	require.NoError(t, err)
	result, err := fixture.store.Start(context.Background(), other.ID, "second")
	require.NoError(t, err)
	require.True(t, result.WorkspaceWarning)
}

func TestStoreGetAggregatesSnapshot(t *testing.T) {
	fixture := newManagerFixture(t)
	result, err := fixture.store.Start(context.Background(), fixture.sessionID, "snapshot")
	require.NoError(t, err)
	runID := result.Run.ID
	_, err = fixture.events.Append(context.Background(), runID, "run.started", map[string]any{})
	require.NoError(t, err)
	_, err = fixture.events.Append(context.Background(), runID, "message.started", map[string]any{})
	require.NoError(t, err)
	now := time.Now().UTC()
	later := now.Add(time.Second)
	first := persistence.ToolCall{
		ID: uuid.New(), RunID: runID, Name: "read", Status: "succeeded",
		ArgumentsJSON: datatypes.JSON(`{}`), CreatedAt: now,
	}
	second := persistence.ToolCall{
		ID: uuid.New(), RunID: runID, Name: "write", Status: "running",
		ArgumentsJSON: datatypes.JSON(`{}`), CreatedAt: later,
	}
	require.NoError(t, fixture.db.Create(&first).Error)
	require.NoError(t, fixture.db.Create(&second).Error)
	approval := persistence.Approval{
		ID: uuid.New(), RunID: runID, ToolCallID: second.ID, Status: "pending",
		RequestedPayloadJSON: datatypes.JSON(`{}`), RequestedAt: now,
	}
	require.NoError(t, fixture.db.Create(&approval).Error)
	snapshot, err := fixture.store.Get(context.Background(), runID)
	require.NoError(t, err)
	require.Len(t, snapshot.ToolCalls, 2)
	require.Equal(t, "read", snapshot.ToolCalls[0].Name)
	require.Equal(t, "write", snapshot.ToolCalls[1].Name)
	require.Len(t, snapshot.Approvals, 1)
	require.Equal(t, approval.ID, snapshot.Approvals[0].ID)
	require.Equal(t, int64(2), snapshot.LastEventSeq)
	require.Equal(t, snapshot.Run.NextEventSeq-1, snapshot.LastEventSeq)
	exists, err := fixture.store.Exists(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = fixture.store.Exists(context.Background(), uuid.New())
	require.NoError(t, err)
	require.False(t, exists)
}

func TestStoreGetUnknownRun(t *testing.T) {
	fixture := newManagerFixture(t)
	_, err := fixture.store.Get(context.Background(), uuid.New())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStoreCancelLeavesSnapshotTerminal(t *testing.T) {
	fixture := newManagerFixture(t)
	result, err := fixture.store.Start(context.Background(), fixture.sessionID, "cancel")
	require.NoError(t, err)
	snapshot, err := fixture.store.Cancel(context.Background(), result.Run.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCancelled, snapshot.Run.Status)
	require.NotNil(t, snapshot.Run.FinishedAt)
	snapshot, err = fixture.store.Cancel(context.Background(), result.Run.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCancelled, snapshot.Run.Status)
	require.Equal(t, int64(1), countRunEvents(t, fixture.db, result.Run.ID, eventRunCancelled))
}

func TestStoreFinishPersistsErrorReason(t *testing.T) {
	fixture := newManagerFixture(t)
	result, err := fixture.store.Start(context.Background(), fixture.sessionID, "fail")
	require.NoError(t, err)
	err = fixture.store.Finish(context.Background(), FinishInput{
		RunID:         result.Run.ID,
		Status:        StatusFailed,
		MessageStatus: messageFailed,
		ErrorCode:     "model_round_limit",
		ErrorMessage:  "model round limit exceeded",
		EventType:     eventRunFailed,
	})
	require.NoError(t, err)
	snapshot, err := fixture.store.Get(context.Background(), result.Run.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Run.ErrorCode)
	require.Equal(t, "model_round_limit", *snapshot.Run.ErrorCode)
	require.NotNil(t, snapshot.Run.ErrorMessage)
	require.Equal(t, "model round limit exceeded", *snapshot.Run.ErrorMessage)
}

func TestStoreStartHealthCheckFailureWritesNothing(t *testing.T) {
	fixture := newManagerFixture(t)
	store := NewStore(fixture.db, fixture.events, failingChecker{})
	_, err := store.Start(context.Background(), fixture.sessionID, "hello")
	require.Error(t, err)
	require.Zero(t, countSessionRuns(t, fixture.db, fixture.sessionID))
	require.Zero(t, countSessionMessages(t, fixture.db, fixture.sessionID))
}

func TestStoreStartCanceledContextWritesNothing(t *testing.T) {
	fixture := newManagerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixture.store.Start(ctx, fixture.sessionID, "hello")
	require.Error(t, err)
	require.Zero(t, countSessionRuns(t, fixture.db, fixture.sessionID))
	require.Zero(t, countSessionMessages(t, fixture.db, fixture.sessionID))
}

func countSessionRuns(t *testing.T, db *gorm.DB, sessionID uuid.UUID) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&persistence.Run{}).
		Where("session_id = ?", sessionID).Count(&count).Error)
	return count
}

func countSessionMessages(t *testing.T, db *gorm.DB, sessionID uuid.UUID) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&persistence.Message{}).
		Where("session_id = ?", sessionID).Count(&count).Error)
	return count
}
