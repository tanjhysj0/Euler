//go:build integration

package run

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

func TestWaitingApprovalRunTimesOut(t *testing.T) {
	fixture := newManagerFixture(t)
	manager := NewManager(
		fixture.store, newFakeRunner(), Limits{MaxDuration: time.Second}, Deps{},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, manager.Shutdown(ctx))
	})
	started, err := manager.Start(context.Background(), fixture.sessionID, "waiting")
	require.NoError(t, err)
	waitForRunStatus(t, fixture.store, started.Run.ID, StatusRunning)
	setRunStatus(t, fixture.db, started.Run.ID, StatusWaitingApproval)
	snapshot := waitForRunStatus(t, fixture.store, started.Run.ID, StatusFailed)
	require.NotNil(t, snapshot.Run.ErrorCode)
	require.Equal(t, codeRunTimeout, *snapshot.Run.ErrorCode)
	require.Equal(t, int64(1), countRunEvents(t, fixture.db, started.Run.ID, eventRunFailed))
}

func TestWaitingApprovalRunCancels(t *testing.T) {
	fixture := newManagerFixture(t)
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "waiting")
	require.NoError(t, err)
	waitForRunStatus(t, fixture.store, started.Run.ID, StatusRunning)
	setRunStatus(t, fixture.db, started.Run.ID, StatusWaitingApproval)
	require.NoError(t, fixture.manager.Cancel(context.Background(), started.Run.ID))
	snapshot := waitForRunStatus(t, fixture.store, started.Run.ID, StatusCancelled)
	require.Equal(t, StatusCancelled, snapshot.Run.Status)
	require.Equal(t, int64(1), countRunEvents(t, fixture.db, started.Run.ID, eventRunCancelled))
}

func TestHandlerMapsActiveRunConflict(t *testing.T) {
	fixture := newManagerFixture(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(fixture.manager, fixture.store, fixture.events).
		Register(router.Group("/api/v1"))
	path := "/api/v1/sessions/" + fixture.sessionID.String() + "/runs"
	first := postJSON(t, router, path, `{"content":"first"}`)
	require.Equal(t, http.StatusAccepted, first.Code)
	var created StartResult
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &created))
	require.NotEqual(t, uuid.Nil, created.Run.ID)
	second := postJSON(t, router, path, `{"content":"second"}`)
	require.Equal(t, http.StatusConflict, second.Code)
	var body struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &body))
	require.Equal(t, codeActiveRunExists, body.Code)
	invalid := postJSON(t, router, "/api/v1/sessions/not-a-uuid/runs", `{"content":"x"}`)
	require.Equal(t, http.StatusBadRequest, invalid.Code)
	require.NoError(t, fixture.manager.Cancel(context.Background(), created.Run.ID))
}

func TestManagerShutdownInterruptsActiveRuns(t *testing.T) {
	fixture := newManagerFixture(t)
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "stop")
	require.NoError(t, err)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, fixture.manager.Shutdown(shutdownCtx))
	snapshot, err := fixture.store.Get(context.Background(), started.Run.ID)
	require.NoError(t, err)
	require.Equal(t, StatusInterrupted, snapshot.Run.Status)
	var message persistence.Message
	err = fixture.db.First(&message, "id = ?", started.AssistantMessage.ID).Error
	require.NoError(t, err)
	require.Equal(t, messageInterrupted, message.Status)
	require.Equal(t, int64(1), countRunEvents(t, fixture.db, started.Run.ID, eventRunInterrupted))
}

func TestShutdownKeepsCancelledRunTerminal(t *testing.T) {
	fixture := newManagerFixture(t)
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "cancel first")
	require.NoError(t, err)
	require.NoError(t, fixture.manager.Cancel(context.Background(), started.Run.ID))
	waitForRunStatus(t, fixture.store, started.Run.ID, StatusCancelled)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, fixture.manager.Shutdown(shutdownCtx))
	snapshot, err := fixture.store.Get(context.Background(), started.Run.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCancelled, snapshot.Run.Status)
	require.Equal(t, int64(0), countRunEvents(t, fixture.db, started.Run.ID, eventRunInterrupted))
}

func TestShutdownInterruptsWaitingApprovalRun(t *testing.T) {
	fixture := newManagerFixture(t)
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "stop waiting")
	require.NoError(t, err)
	waitForRunStatus(t, fixture.store, started.Run.ID, StatusRunning)
	setRunStatus(t, fixture.db, started.Run.ID, StatusWaitingApproval)
	now := time.Now().UTC()
	toolCall := persistence.ToolCall{
		ID: uuid.New(), RunID: started.Run.ID, Name: "shell",
		ArgumentsJSON: datatypes.JSON(`{}`), Status: "running",
		CreatedAt: now, StartedAt: &now,
	}
	require.NoError(t, fixture.db.Create(&toolCall).Error)
	approval := persistence.Approval{
		ID: uuid.New(), RunID: started.Run.ID, ToolCallID: toolCall.ID,
		Status: "pending", RequestedPayloadJSON: datatypes.JSON(`{}`),
		RequestedAt: now,
	}
	require.NoError(t, fixture.db.Create(&approval).Error)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, fixture.manager.Shutdown(shutdownCtx))
	snapshot := waitForRunStatus(t, fixture.store, started.Run.ID, StatusInterrupted)
	require.Len(t, snapshot.ToolCalls, 1)
	require.Equal(t, "cancelled", snapshot.ToolCalls[0].Status)
	require.Len(t, snapshot.Approvals, 1)
	require.Equal(t, "cancelled", snapshot.Approvals[0].Status)
	require.Equal(t, int64(1), countRunEvents(t, fixture.db, started.Run.ID, eventRunInterrupted))
}

func TestRecoverInterruptedRepairsState(t *testing.T) {
	fixture := newManagerFixture(t)
	rows := insertRunningRun(t, fixture.db, fixture.sessionID)
	require.NoError(t, fixture.manager.RecoverInterrupted(context.Background()))
	snapshot, err := fixture.store.Get(context.Background(), rows.runID)
	require.NoError(t, err)
	require.Equal(t, StatusInterrupted, snapshot.Run.Status)
	require.NotNil(t, snapshot.Run.FinishedAt)
	var message persistence.Message
	err = fixture.db.First(&message, "id = ?", rows.messageID).Error
	require.NoError(t, err)
	require.Equal(t, messageInterrupted, message.Status)
	var approval persistence.Approval
	err = fixture.db.First(&approval, "id = ?", rows.approvalID).Error
	require.NoError(t, err)
	require.Equal(t, "cancelled", approval.Status)
	require.NotNil(t, approval.DecidedAt)
	var toolCall persistence.ToolCall
	err = fixture.db.First(&toolCall, "id = ?", rows.toolCallID).Error
	require.NoError(t, err)
	require.Equal(t, "cancelled", toolCall.Status)
	require.NotNil(t, toolCall.FinishedAt)
	require.Equal(t, int64(1), countRunEvents(t, fixture.db, rows.runID, eventRunInterrupted))
}

func TestSnapshotLastEventSeqMatchesEvents(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.runner.block = false
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "snapshot")
	require.NoError(t, err)
	waitForRunStatus(t, fixture.store, started.Run.ID, StatusSucceeded)
	require.Eventually(t, func() bool {
		snapshot, getErr := fixture.store.Get(context.Background(), started.Run.ID)
		if getErr != nil {
			return false
		}
		return snapshot.LastEventSeq == countAnyRunEvents(t, fixture.db, started.Run.ID)
	}, time.Second, 10*time.Millisecond)
	snapshot, err := fixture.store.Get(context.Background(), started.Run.ID)
	require.NoError(t, err)
	require.Equal(t, snapshot.Run.NextEventSeq-1, snapshot.LastEventSeq)
}

type runningRunRows struct {
	runID      uuid.UUID
	messageID  uuid.UUID
	approvalID uuid.UUID
	toolCallID uuid.UUID
}

func insertRunningRun(t *testing.T, db *gorm.DB, sessionID uuid.UUID) runningRunRows {
	t.Helper()
	now := time.Now().UTC()
	runID := uuid.New()
	row := persistence.Run{
		ID: runID, SessionID: sessionID, Status: StatusRunning,
		NextEventSeq: 1, CreatedAt: now, StartedAt: &now,
	}
	require.NoError(t, db.Create(&row).Error)
	message := persistence.Message{
		ID: uuid.New(), SessionID: sessionID, RunID: &runID, SessionSeq: 1,
		Role: "assistant", Status: messageStreaming, CreatedAt: now,
	}
	require.NoError(t, db.Create(&message).Error)
	toolCall := persistence.ToolCall{
		ID: uuid.New(), RunID: runID, Name: "shell",
		ArgumentsJSON: datatypes.JSON(`{}`), Status: "running",
		CreatedAt: now, StartedAt: &now,
	}
	require.NoError(t, db.Create(&toolCall).Error)
	approval := persistence.Approval{
		ID: uuid.New(), RunID: runID, ToolCallID: toolCall.ID, Status: "pending",
		RequestedPayloadJSON: datatypes.JSON(`{}`), RequestedAt: now,
	}
	require.NoError(t, db.Create(&approval).Error)
	return runningRunRows{
		runID: runID, messageID: message.ID,
		approvalID: approval.ID, toolCallID: toolCall.ID,
	}
}

func countAnyRunEvents(t *testing.T, db *gorm.DB, runID uuid.UUID) int64 {
	t.Helper()
	var count int64
	err := db.Model(&persistence.RunEvent{}).Where("run_id = ?", runID).Count(&count).Error
	require.NoError(t, err)
	return count
}

func postJSON(
	t *testing.T, router http.Handler, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}
