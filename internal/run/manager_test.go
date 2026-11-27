//go:build integration

package run

import (
	"context"
	"errors"
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
)

const testDatabaseURLEnv = "TEST_DATABASE_URL"

func TestRunTransitions(t *testing.T) {
	require.True(t, CanTransition("queued", "running"))
	require.True(t, CanTransition("running", "waiting_approval"))
	require.True(t, CanTransition("waiting_approval", "running"))
	require.True(t, CanTransition("running", "succeeded"))
	require.False(t, CanTransition("succeeded", "running"))
	require.False(t, CanTransition("cancelled", "waiting_approval"))
	require.False(t, CanTransition("queued", "interrupted"))
	require.True(t, CanTransition("queued", "failed"))
	require.True(t, CanTransition("waiting_approval", "failed"))
	require.True(t, CanTransition("waiting_approval", "cancelled"))
	require.False(t, CanTransition("waiting_approval", "interrupted"))
}

func TestManagerRejectsSecondActiveRun(t *testing.T) {
	fixture := newManagerFixture(t)
	first, err := fixture.manager.Start(context.Background(), fixture.sessionID, "first")
	require.NoError(t, err)
	_, err = fixture.manager.Start(context.Background(), fixture.sessionID, "second")
	require.ErrorIs(t, err, ErrActiveRun)
	require.NoError(t, fixture.manager.Cancel(context.Background(), first.Run.ID))
}

func TestDifferentSessionsRunConcurrently(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.runner.block = true
	_, err := fixture.manager.Start(context.Background(), fixture.sessionA, "a")
	require.NoError(t, err)
	_, err = fixture.manager.Start(context.Background(), fixture.sessionB, "b")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return fixture.runner.ActiveCount() == 2
	}, time.Second, 10*time.Millisecond)
}

func TestManagerStartsNewRunAfterCancel(t *testing.T) {
	fixture := newManagerFixture(t)
	first, err := fixture.manager.Start(context.Background(), fixture.sessionID, "first")
	require.NoError(t, err)
	require.NoError(t, fixture.manager.Cancel(context.Background(), first.Run.ID))
	snapshot, err := fixture.store.Get(context.Background(), first.Run.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCancelled, snapshot.Run.Status)
	second, err := fixture.manager.Start(context.Background(), fixture.sessionID, "second")
	require.NoError(t, err)
	require.NotEqual(t, first.Run.ID, second.Run.ID)
	require.NoError(t, fixture.manager.Cancel(context.Background(), second.Run.ID))
}

func TestManagerCompletesRun(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.runner.block = false
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "hello")
	require.NoError(t, err)
	snapshot := waitForRunStatus(t, fixture.store, started.Run.ID, StatusSucceeded)
	require.Equal(t, StatusSucceeded, snapshot.Run.Status)
	var message persistence.Message
	err = fixture.db.First(&message, "id = ?", started.AssistantMessage.ID).Error
	require.NoError(t, err)
	require.Equal(t, messageCompleted, message.Status)
	require.NotNil(t, message.CompletedAt)
	require.Eventually(t, func() bool {
		return countRunEvents(t, fixture.db, started.Run.ID, eventRunCompleted) == 1
	}, time.Second, 10*time.Millisecond)
}

func TestManagerFailsRun(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.runner.block = false
	fixture.runner.result = errors.New("model exploded")
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "hello")
	require.NoError(t, err)
	snapshot := waitForRunStatus(t, fixture.store, started.Run.ID, StatusFailed)
	require.NotNil(t, snapshot.Run.ErrorCode)
	require.Equal(t, codeRunFailed, *snapshot.Run.ErrorCode)
	var message persistence.Message
	err = fixture.db.First(&message, "id = ?", started.AssistantMessage.ID).Error
	require.NoError(t, err)
	require.Equal(t, messageFailed, message.Status)
	require.Eventually(t, func() bool {
		return countRunEvents(t, fixture.db, started.Run.ID, eventRunFailed) == 1
	}, time.Second, 10*time.Millisecond)
}

func TestManagerMapsCodedErrorCode(t *testing.T) {
	fixture := newManagerFixture(t)
	fixture.runner.block = false
	fixture.runner.result = codedRunError{code: "model_auth"}
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "hello")
	require.NoError(t, err)
	snapshot := waitForRunStatus(t, fixture.store, started.Run.ID, StatusFailed)
	require.NotNil(t, snapshot.Run.ErrorCode)
	require.Equal(t, "model_auth", *snapshot.Run.ErrorCode)
	var message persistence.Message
	err = fixture.db.First(&message, "id = ?", started.AssistantMessage.ID).Error
	require.NoError(t, err)
	require.Equal(t, messageFailed, message.Status)
	require.Eventually(t, func() bool {
		return countRunEvents(t, fixture.db, started.Run.ID, eventRunFailed) == 1
	}, time.Second, 10*time.Millisecond)
}

type codedRunError struct{ code string }

func (codedRunError) Error() string       { return "coded failure" }
func (e codedRunError) ErrorCode() string { return e.code }

func TestManagerCancelIsIdempotent(t *testing.T) {
	fixture := newManagerFixture(t)
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "cancel me")
	require.NoError(t, err)
	require.NoError(t, fixture.manager.Cancel(context.Background(), started.Run.ID))
	require.Eventually(t, func() bool {
		return countRunEvents(t, fixture.db, started.Run.ID, eventRunCancelled) == 1
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, fixture.manager.Cancel(context.Background(), started.Run.ID))
	require.Equal(t, int64(1), countRunEvents(t, fixture.db, started.Run.ID, eventRunCancelled))
	snapshot, err := fixture.store.Get(context.Background(), started.Run.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCancelled, snapshot.Run.Status)
}

func TestManagerEmitsOrderedLifecycleEvents(t *testing.T) {
	fixture := newManagerFixture(t)
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "ordered")
	require.NoError(t, err)
	waitForRunStatus(t, fixture.store, started.Run.ID, StatusRunning)
	require.NoError(t, fixture.manager.Cancel(context.Background(), started.Run.ID))
	require.Equal(t,
		[]string{eventRunStarted, eventMessageStarted, eventRunCancelled},
		runEventTypes(t, fixture.db, started.Run.ID))
}

func TestCancelCancelsApprovalAndToolCall(t *testing.T) {
	fixture := newManagerFixture(t)
	started, err := fixture.manager.Start(context.Background(), fixture.sessionID, "cancel tools")
	require.NoError(t, err)
	waitForRunStatus(t, fixture.store, started.Run.ID, StatusRunning)
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
	require.NoError(t, fixture.manager.Cancel(context.Background(), started.Run.ID))
	snapshot, err := fixture.store.Get(context.Background(), started.Run.ID)
	require.NoError(t, err)
	require.Len(t, snapshot.ToolCalls, 1)
	require.Equal(t, "cancelled", snapshot.ToolCalls[0].Status)
	require.Len(t, snapshot.Approvals, 1)
	require.Equal(t, "cancelled", snapshot.Approvals[0].Status)
}

type fakeRunner struct {
	mu      sync.Mutex
	active  int
	block   bool
	release chan struct{}
	result  error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{block: true, release: make(chan struct{})}
}

func (f *fakeRunner) Run(ctx context.Context, _ Request) error {
	f.mu.Lock()
	f.active++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.block {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.release:
		}
	}
	return f.result
}

func (f *fakeRunner) ActiveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}

type managerFixture struct {
	db        *gorm.DB
	manager   *Manager
	store     *Store
	events    *event.Store
	runner    *fakeRunner
	sessionID uuid.UUID
	sessionA  uuid.UUID
	sessionB  uuid.UUID
}

func newManagerFixture(t *testing.T) *managerFixture {
	t.Helper()
	db := openRunTestDB(t)
	sessionID := newRunSession(t, db)
	sessionA := newRunSession(t, db)
	sessionB := newRunSession(t, db)
	events := event.NewStore(db, event.NewHub())
	store := NewStore(db, events, nil)
	runner := newFakeRunner()
	manager := NewManager(store, runner, Limits{MaxDuration: 10 * time.Second}, Deps{})
	// 在会话清理之后注册，确保 worker 先停止再删除数据行。
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("shutdown manager: %v", err)
		}
	})
	return &managerFixture{
		db:        db,
		manager:   manager,
		store:     store,
		events:    events,
		runner:    runner,
		sessionID: sessionID,
		sessionA:  sessionA,
		sessionB:  sessionB,
	}
}

func openRunTestDB(t *testing.T) *gorm.DB {
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

func newRunSession(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	now := time.Now().UTC()
	workspace := persistence.Workspace{ID: uuid.New(), Path: t.TempDir(), CreatedAt: now}
	require.NoError(t, db.Create(&workspace).Error)
	profile := persistence.ModelProfile{
		ID:         uuid.New(),
		Name:       "run integration",
		BaseURL:    "http://127.0.0.1:1/v1",
		Model:      "test-model",
		SecretKind: "environment",
		SecretRef:  "TEST_API_KEY",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	require.NoError(t, db.Create(&profile).Error)
	session := persistence.Session{
		ID:             uuid.New(),
		WorkspaceID:    workspace.ID,
		ModelProfileID: profile.ID,
		Title:          "run fixture",
		Status:         "active",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActiveAt:   now,
	}
	require.NoError(t, db.Create(&session).Error)
	t.Cleanup(func() {
		deleteRunFixtures(t, db, workspace.ID, profile.ID)
	})
	return session.ID
}

// deleteRunFixtures 只删除属于单个测试工作区的数据行。删除遵循外键顺序，
// 绝不清空共享表。
func deleteRunFixtures(t *testing.T, db *gorm.DB, workspaceID, profileID uuid.UUID) {
	t.Helper()
	var sessionIDs []uuid.UUID
	require.NoError(t, db.Model(&persistence.Session{}).
		Where("workspace_id = ?", workspaceID).Pluck("id", &sessionIDs).Error)
	var runIDs []uuid.UUID
	if len(sessionIDs) > 0 {
		require.NoError(t, db.Model(&persistence.Run{}).
			Where("session_id IN ?", sessionIDs).Pluck("id", &runIDs).Error)
	}
	if len(runIDs) > 0 {
		require.NoError(t, db.Where("run_id IN ?", runIDs).
			Delete(&persistence.Approval{}).Error)
		require.NoError(t, db.Where("run_id IN ?", runIDs).
			Delete(&persistence.ToolCall{}).Error)
		require.NoError(t, db.Where("run_id IN ?", runIDs).
			Delete(&persistence.RunEvent{}).Error)
		require.NoError(t, db.Where("run_id IN ?", runIDs).
			Delete(&persistence.Message{}).Error)
		require.NoError(t, db.Where("id IN ?", runIDs).
			Delete(&persistence.Run{}).Error)
	}
	if len(sessionIDs) > 0 {
		require.NoError(t, db.Where("session_id IN ?", sessionIDs).
			Delete(&persistence.Message{}).Error)
		require.NoError(t, db.Where("id IN ?", sessionIDs).
			Delete(&persistence.Session{}).Error)
	}
	require.NoError(t, db.Where("id = ?", workspaceID).
		Delete(&persistence.Workspace{}).Error)
	require.NoError(t, db.Where("id = ?", profileID).
		Delete(&persistence.ModelProfile{}).Error)
}

func waitForRunStatus(t *testing.T, store *Store, runID uuid.UUID, status string) Snapshot {
	t.Helper()
	var snapshot Snapshot
	require.Eventually(t, func() bool {
		got, err := store.Get(context.Background(), runID)
		if err != nil {
			return false
		}
		snapshot = got
		return got.Run.Status == status
	}, 5*time.Second, 10*time.Millisecond)
	return snapshot
}

func countRunEvents(t *testing.T, db *gorm.DB, runID uuid.UUID, eventType string) int64 {
	t.Helper()
	var count int64
	err := db.Model(&persistence.RunEvent{}).
		Where("run_id = ? AND type = ?", runID, eventType).Count(&count).Error
	require.NoError(t, err)
	return count
}

func runEventTypes(t *testing.T, db *gorm.DB, runID uuid.UUID) []string {
	t.Helper()
	var rows []persistence.RunEvent
	require.NoError(t, db.Where("run_id = ?", runID).Order("seq ASC").Find(&rows).Error)
	types := make([]string, len(rows))
	for index, row := range rows {
		types[index] = row.Type
	}
	return types
}

func setRunStatus(t *testing.T, db *gorm.DB, runID uuid.UUID, status string) {
	t.Helper()
	update := db.Model(&persistence.Run{}).Where("id = ?", runID).Update("status", status)
	require.NoError(t, update.Error)
}
