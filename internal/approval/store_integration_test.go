//go:build integration

package approval

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"euler/internal/event"
	"euler/internal/persistence"
	"euler/internal/run"
)

const testDatabaseURLEnv = "TEST_DATABASE_URL"

type dbFixture struct {
	db         *gorm.DB
	store      *DBStore
	events     *event.Store
	runs       *run.Store
	runID      uuid.UUID
	toolCallID uuid.UUID
}

// newDBFixture 提交一个处于运行中的 run 及其 pending 工具调用。
// 清理时按外键顺序删除各行；不做任何 truncate。
func newDBFixture(t *testing.T) *dbFixture {
	t.Helper()
	db := openApprovalTestDB(t)
	now := time.Now().UTC()
	workspace := persistence.Workspace{ID: uuid.New(), Path: t.TempDir(), CreatedAt: now}
	require.NoError(t, db.Create(&workspace).Error)
	profile := persistence.ModelProfile{
		ID: uuid.New(), Name: "approval integration",
		BaseURL: "http://127.0.0.1:1/v1", Model: "test-model",
		SecretKind: "environment", SecretRef: "TEST_API_KEY",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&profile).Error)
	session := persistence.Session{
		ID: uuid.New(), WorkspaceID: workspace.ID, ModelProfileID: profile.ID,
		Title: "approval fixture", Status: "active",
		CreatedAt: now, UpdatedAt: now, LastActiveAt: now,
	}
	require.NoError(t, db.Create(&session).Error)
	runRow := persistence.Run{
		ID: uuid.New(), SessionID: session.ID, Status: run.StatusRunning,
		NextEventSeq: 1, CreatedAt: now, StartedAt: &now,
	}
	require.NoError(t, db.Create(&runRow).Error)
	toolCall := persistence.ToolCall{
		ID: uuid.New(), RunID: runRow.ID, Name: "shell",
		ArgumentsJSON: datatypes.JSON(`{}`), Status: "pending", CreatedAt: now,
	}
	require.NoError(t, db.Create(&toolCall).Error)
	t.Cleanup(func() {
		deleteApprovalFixtures(t, db, fixtureIDs{
			runID: runRow.ID, sessionID: session.ID,
			workspaceID: workspace.ID, profileID: profile.ID,
		})
	})
	events := event.NewStore(db, event.NewHub())
	return &dbFixture{
		db: db, store: NewDBStore(db), events: events,
		runs:  run.NewStore(db, events, nil),
		runID: runRow.ID, toolCallID: toolCall.ID,
	}
}

func openApprovalTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		// 未指定共享测试库时，使用每个测试独立的临时 SQLite 文件，
		// 无需外部数据库服务，测试间完全隔离。
		databaseURL = filepath.Join(t.TempDir(), "test.db")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	t.Chdir(repoRoot)
	require.NoError(t, persistence.Migrate("", databaseURL))
	db, err := persistence.Open(context.Background(), "", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr != nil {
			t.Errorf("get sql db: %v", dbErr)
			return
		}
		if closeErr := sqlDB.Close(); closeErr != nil {
			t.Errorf("close sql db: %v", closeErr)
		}
	})
	return db
}

// fixtureIDs 按外键删除顺序列出一个 fixture 拥有的各行。
type fixtureIDs struct {
	runID       uuid.UUID
	sessionID   uuid.UUID
	workspaceID uuid.UUID
	profileID   uuid.UUID
}

func deleteApprovalFixtures(t *testing.T, db *gorm.DB, ids fixtureIDs) {
	t.Helper()
	// tool_calls.approval_id 和 approvals.tool_call_id 互相引用；
	// 删除任一侧之前先打破这个循环。
	require.NoError(t, db.Model(&persistence.ToolCall{}).Where("run_id = ?", ids.runID).
		Update("approval_id", nil).Error)
	require.NoError(t, db.Where("run_id = ?", ids.runID).Delete(&persistence.Approval{}).Error)
	require.NoError(t, db.Where("run_id = ?", ids.runID).Delete(&persistence.ToolCall{}).Error)
	require.NoError(t, db.Where("run_id = ?", ids.runID).Delete(&persistence.RunEvent{}).Error)
	require.NoError(t, db.Where("id = ?", ids.runID).Delete(&persistence.Run{}).Error)
	require.NoError(t, db.Where("id = ?", ids.sessionID).Delete(&persistence.Session{}).Error)
	require.NoError(t, db.Where("id = ?", ids.workspaceID).Delete(&persistence.Workspace{}).Error)
	require.NoError(t, db.Where("id = ?", ids.profileID).Delete(&persistence.ModelProfile{}).Error)
}

func (f *dbFixture) request() Request {
	return Request{
		ID: uuid.New(), RunID: f.runID, ToolCallID: f.toolCallID,
		Name: "shell", Payload: map[string]any{"command": "pwd"},
	}
}

func runStatus(t *testing.T, db *gorm.DB, runID uuid.UUID) string {
	t.Helper()
	var row persistence.Run
	require.NoError(t, db.First(&row, "id = ?", runID).Error)
	return row.Status
}

func toolCallRow(t *testing.T, db *gorm.DB, id uuid.UUID) persistence.ToolCall {
	t.Helper()
	var row persistence.ToolCall
	require.NoError(t, db.First(&row, "id = ?", id).Error)
	return row
}

func approvalEventTypes(t *testing.T, db *gorm.DB, runID uuid.UUID) []string {
	t.Helper()
	var rows []persistence.RunEvent
	require.NoError(t, db.Where("run_id = ?", runID).Order("seq ASC").Find(&rows).Error)
	types := make([]string, len(rows))
	for index, row := range rows {
		types[index] = row.Type
	}
	return types
}

func TestOpenParksRunAndToolCall(t *testing.T) {
	fixture := newDBFixture(t)
	request := fixture.request()
	created, err := fixture.store.Open(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, EventRequested, created.Type)
	require.Equal(t, int64(1), created.Seq)
	require.Equal(t, run.StatusWaitingApproval, runStatus(t, fixture.db, fixture.runID))
	call := toolCallRow(t, fixture.db, fixture.toolCallID)
	require.Equal(t, run.StatusWaitingApproval, call.Status)
	require.NotNil(t, call.ApprovalID)
	require.Equal(t, request.ID, *call.ApprovalID)
	record, err := fixture.store.Get(context.Background(), request.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, record.Status)
}

func TestOpenRejectsNonRunningRun(t *testing.T) {
	fixture := newDBFixture(t)
	require.NoError(t, fixture.db.Model(&persistence.Run{}).
		Where("id = ?", fixture.runID).Update("status", run.StatusQueued).Error)
	_, err := fixture.store.Open(context.Background(), fixture.request())
	require.ErrorIs(t, err, ErrInvalidRequest)
	require.Equal(t, run.StatusQueued, runStatus(t, fixture.db, fixture.runID))
	require.Empty(t, approvalEventTypes(t, fixture.db, fixture.runID))
	require.Equal(t, "pending", toolCallRow(t, fixture.db, fixture.toolCallID).Status)
}

func TestOpenRejectsForeignToolCall(t *testing.T) {
	fixture := newDBFixture(t)
	request := fixture.request()
	request.ToolCallID = uuid.New()
	_, err := fixture.store.Open(context.Background(), request)
	require.ErrorIs(t, err, ErrInvalidRequest)
	require.Equal(t, run.StatusRunning, runStatus(t, fixture.db, fixture.runID))
	require.Empty(t, approvalEventTypes(t, fixture.db, fixture.runID))
}

func TestResolveApprovedResumesRun(t *testing.T) {
	fixture := newDBFixture(t)
	request := fixture.request()
	_, err := fixture.store.Open(context.Background(), request)
	require.NoError(t, err)
	record, resolved, err := fixture.store.Resolve(context.Background(), request.ID,
		Resolution{Status: StatusApproved, Approved: true, Reason: "ok"})
	require.NoError(t, err)
	require.Equal(t, EventResolved, resolved.Type)
	require.Equal(t, StatusApproved, record.Status)
	require.True(t, record.Decision.Approved)
	require.NotNil(t, record.DecidedAt)
	require.Equal(t, run.StatusRunning, runStatus(t, fixture.db, fixture.runID))
	require.Equal(t, "running", toolCallRow(t, fixture.db, fixture.toolCallID).Status)
	require.Equal(t, []string{EventRequested, EventResolved},
		approvalEventTypes(t, fixture.db, fixture.runID))
	_, _, err = fixture.store.Resolve(context.Background(), request.ID,
		Resolution{Status: StatusRejected})
	require.ErrorIs(t, err, ErrAlreadyDecided)
}

func TestResolveRejectedFinishesToolCall(t *testing.T) {
	fixture := newDBFixture(t)
	request := fixture.request()
	_, err := fixture.store.Open(context.Background(), request)
	require.NoError(t, err)
	record, _, err := fixture.store.Resolve(context.Background(), request.ID,
		Resolution{Status: StatusRejected, Reason: "不允许"})
	require.NoError(t, err)
	require.False(t, record.Decision.Approved)
	require.Equal(t, "不允许", record.Decision.Reason)
	require.Equal(t, run.StatusRunning, runStatus(t, fixture.db, fixture.runID))
	call := toolCallRow(t, fixture.db, fixture.toolCallID)
	require.Equal(t, StatusRejected, call.Status)
	require.NotNil(t, call.FinishedAt)
}

func TestResolveKeepsCancelledRunUntouched(t *testing.T) {
	fixture := newDBFixture(t)
	request := fixture.request()
	_, err := fixture.store.Open(context.Background(), request)
	require.NoError(t, err)
	_, err = fixture.runs.Cancel(context.Background(), fixture.runID)
	require.NoError(t, err)
	// Cancel 已经决策了该 pending 审批，因此迟到的决策在
	// “首次决策生效”的竞争中落败，绝不能复活 run 或工具调用。
	record, _, err := fixture.store.Resolve(context.Background(), request.ID,
		Resolution{Status: StatusApproved, Approved: true})
	require.ErrorIs(t, err, ErrAlreadyDecided)
	require.Equal(t, StatusCancelled, record.Status)
	require.Equal(t, run.StatusCancelled, runStatus(t, fixture.db, fixture.runID))
	require.Equal(t, "cancelled", toolCallRow(t, fixture.db, fixture.toolCallID).Status)
}

func TestResolveUnknownApproval(t *testing.T) {
	fixture := newDBFixture(t)
	_, _, err := fixture.store.Resolve(context.Background(), uuid.New(),
		Resolution{Status: StatusApproved, Approved: true})
	require.ErrorIs(t, err, ErrNotFound)
}

// TestResolveAndCancelLockOrder 让决策与运行取消相互竞争。
// 两个事务都必须按 run → approval → tool call 的顺序加锁；顺序颠倒
// 会在这里表现为死锁或写锁超时，而不是干净的结果。
func TestResolveAndCancelLockOrder(t *testing.T) {
	fixture := newDBFixture(t)
	for range 10 {
		resetApprovalFixture(t, fixture)
		request := fixture.request()
		_, err := fixture.store.Open(context.Background(), request)
		require.NoError(t, err)
		var resolveErr, cancelErr error
		var wg sync.WaitGroup
		wg.Go(func() {
			_, _, resolveErr = fixture.store.Resolve(context.Background(), request.ID,
				Resolution{Status: StatusApproved, Approved: true})
		})
		wg.Go(func() {
			_, cancelErr = fixture.runs.Cancel(context.Background(), fixture.runID)
		})
		wg.Wait()
		if resolveErr != nil {
			require.ErrorIs(t, resolveErr, ErrAlreadyDecided)
		}
		require.NoError(t, cancelErr)
		require.Equal(t, run.StatusCancelled, runStatus(t, fixture.db, fixture.runID))
	}
}

// resetApprovalFixture 将 fixture 恢复为一个运行中的 run、一个 pending
// 工具调用且没有审批的状态，使下一轮迭代可以创建新的审批。
func resetApprovalFixture(t *testing.T, fixture *dbFixture) {
	t.Helper()
	require.NoError(t, fixture.db.Model(&persistence.ToolCall{}).
		Where("run_id = ?", fixture.runID).
		Updates(map[string]any{
			"status": "pending", "approval_id": nil, "finished_at": nil,
		}).Error)
	require.NoError(t, fixture.db.Where("run_id = ?", fixture.runID).
		Delete(&persistence.Approval{}).Error)
	require.NoError(t, fixture.db.Model(&persistence.Run{}).
		Where("id = ?", fixture.runID).
		Updates(map[string]any{
			"status": run.StatusRunning, "finished_at": nil, "error_code": nil,
		}).Error)
}

func TestServiceRequireBlocksUntilDecision(t *testing.T) {
	fixture := newDBFixture(t)
	service := NewService(fixture.store, fixture.events)
	result := make(chan requireResult, 1)
	go func() {
		request := fixture.request()
		request.ID = uuid.Nil
		decision, err := service.Require(context.Background(), request)
		result <- requireResult{decision: decision, err: err}
	}()
	var approvalID uuid.UUID
	found := false
	for range 500 {
		var rows []persistence.Approval
		err := fixture.db.Where("run_id = ? AND status = ?", fixture.runID, StatusPending).
			Find(&rows).Error
		if err == nil && len(rows) == 1 {
			approvalID = rows[0].ID
			found = true
			break
		}
		select {
		case got := <-result:
			t.Fatalf("require returned early: decision=%+v err=%v", got.decision, got.err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, found, "pending approval never appeared")
	require.Never(t, func() bool { return len(result) > 0 },
		100*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, run.StatusWaitingApproval, runStatus(t, fixture.db, fixture.runID))
	_, err := service.Decide(context.Background(), approvalID,
		Resolution{Status: StatusApproved, Approved: true, Reason: "ok"})
	require.NoError(t, err)
	got := <-result
	require.NoError(t, got.err)
	require.True(t, got.decision.Approved)
	require.Equal(t, run.StatusRunning, runStatus(t, fixture.db, fixture.runID))
	require.Equal(t, []string{EventRequested, EventResolved},
		approvalEventTypes(t, fixture.db, fixture.runID))
}
