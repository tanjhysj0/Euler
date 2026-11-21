//go:build integration

package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"euler/internal/persistence"
	"euler/internal/workspace"
)

const testDatabaseURLEnv = "TEST_DATABASE_URL"

var testProfileID = uuid.MustParse("6f3c1d52-9a0b-4d3e-8f21-2b7c5d4e1a90")

// 空路径时每个会话自动分配独立目录，当前版本保持“一目录一会话”；
// 未来多会话共享同一目录时改用显式 Path（复用逻辑已在下方用例覆盖）。
func TestCreateSessionAutoAllocatesUniqueWorkspaces(t *testing.T) {
	svc, _ := newSessionServiceForTest(t)
	first, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)
	second, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)
	require.NotEqual(t, first.Workspace.ID, second.Workspace.ID)
	require.NotEqual(t, first.Workspace.Path, second.Workspace.Path)
	require.DirExists(t, first.Workspace.Path)
	require.DirExists(t, second.Workspace.Path)
	require.False(t, first.WorkspaceWarning)
	require.False(t, second.WorkspaceWarning)
}

func TestCreateSessionReusesWorkspacePath(t *testing.T) {
	svc, db := newSessionServiceForTest(t)
	dir := t.TempDir()
	normalized, normalizeErr := svc.workspaces.NormalizeExisting(dir)
	require.NoError(t, normalizeErr)
	first, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID, Path: dir})
	require.NoError(t, err)
	second, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: dir,
	})
	require.NoError(t, err)
	require.Equal(t, first.Workspace.ID, second.Workspace.ID)
	require.NotEqual(t, first.ID, second.ID)
	var count int64
	require.NoError(t, db.Model(&persistence.Workspace{}).
		Where("path = ?", normalized).Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestCreateSessionConcurrentlyReusesSingleWorkspace(t *testing.T) {
	svc, db := newCommittedSessionService(t)
	dir := t.TempDir()
	normalized, err := svc.workspaces.NormalizeExisting(dir)
	require.NoError(t, err)
	profileID := uuid.New()
	profile := persistence.ModelProfile{
		ID:         profileID,
		Name:       "concurrent",
		BaseURL:    "http://127.0.0.1:1/v1",
		Model:      "test-model",
		SecretKind: "environment",
		SecretRef:  "TEST_API_KEY",
	}
	require.NoError(t, db.Create(&profile).Error)
	t.Cleanup(func() {
		var workspaceIDs []uuid.UUID
		require.NoError(t, db.Model(&persistence.Workspace{}).
			Where("path = ?", normalized).Pluck("id", &workspaceIDs).Error)
		if len(workspaceIDs) > 0 {
			require.NoError(t, db.Where("workspace_id IN ?", workspaceIDs).
				Delete(&persistence.Session{}).Error)
		}
		require.NoError(t, db.Where("path = ?", normalized).
			Delete(&persistence.Workspace{}).Error)
		require.NoError(t, db.Delete(&persistence.ModelProfile{}, "id = ?", profileID).Error)
	})
	const creators = 8
	results := make([]CreateResult, creators)
	errs := make([]error, creators)
	var group sync.WaitGroup
	for i := range creators {
		group.Go(func() {
			results[i], errs[i] = svc.Create(context.Background(), CreateInput{
				ProfileID: profileID, Path: dir,
			})
		})
	}
	group.Wait()
	for i := range creators {
		require.NoError(t, errs[i])
	}
	var count int64
	require.NoError(t, db.Model(&persistence.Workspace{}).
		Where("path = ?", normalized).Count(&count).Error)
	require.Equal(t, int64(1), count)
	workspaceID := results[0].Workspace.ID
	for _, result := range results {
		require.Equal(t, workspaceID, result.Workspace.ID)
	}
}

func TestCreateSessionWithEmptyPathCreatesDefaultWorkspace(t *testing.T) {
	svc, _ := newSessionServiceForTest(t)
	created, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)
	require.DirExists(t, created.Workspace.Path)
	require.Equal(t, filepath.Base(created.Workspace.Path), created.Title)
	require.False(t, created.WorkspaceWarning)
}

func TestCreateSessionRejectsRelativePath(t *testing.T) {
	svc, _ := newSessionServiceForTest(t)
	_, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: filepath.Join("relative", "dir"),
	})
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestCreateSessionCleansUpNewDirectoryOnDatabaseFailure(t *testing.T) {
	svc, _, root := newSessionServiceWithRoot(t)
	_, err := svc.Create(context.Background(), CreateInput{ProfileID: uuid.New()})
	require.Error(t, err)
	entries, readErr := os.ReadDir(root)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}

func TestCreateSessionKeepsExistingDirectoryOnFailure(t *testing.T) {
	svc, _, _ := newSessionServiceWithRoot(t)
	dir := t.TempDir()
	_, err := svc.Create(context.Background(), CreateInput{
		ProfileID: uuid.New(), Path: dir,
	})
	require.Error(t, err)
	require.DirExists(t, dir)
}

func TestCreateSessionWarnsWhenWorkspaceHasActiveRun(t *testing.T) {
	svc, db, _ := newSessionServiceWithRoot(t)
	dir := t.TempDir()
	first, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: dir,
	})
	require.NoError(t, err)
	run := persistence.Run{
		ID: uuid.New(), SessionID: first.ID, Status: "running", NextEventSeq: 1,
	}
	require.NoError(t, db.Create(&run).Error)
	second, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: dir,
	})
	require.NoError(t, err)
	require.True(t, second.WorkspaceWarning)
	require.Equal(t, first.Workspace.ID, second.Workspace.ID)
}

func TestListSessionsFiltersByWorkspacePathAndActiveRun(t *testing.T) {
	svc, db, _ := newSessionServiceWithRoot(t)
	activeDir := t.TempDir()
	idleDir := t.TempDir()
	active, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: activeDir,
	})
	require.NoError(t, err)
	idle, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: idleDir,
	})
	require.NoError(t, err)
	run := persistence.Run{
		ID: uuid.New(), SessionID: active.ID, Status: "queued", NextEventSeq: 1,
	}
	require.NoError(t, db.Create(&run).Error)
	byPath, err := svc.List(context.Background(), ListFilter{WorkspacePath: activeDir})
	require.NoError(t, err)
	require.Len(t, byPath, 1)
	require.Equal(t, active.ID, byPath[0].ID)
	byActive, err := svc.List(context.Background(), ListFilter{ActiveRun: true})
	require.NoError(t, err)
	activeIDs := sessionIDs(byActive)
	require.Contains(t, activeIDs, active.ID)
	require.NotContains(t, activeIDs, idle.ID)
	all, err := svc.List(context.Background(), ListFilter{})
	require.NoError(t, err)
	allIDs := sessionIDs(all)
	require.Contains(t, allIDs, active.ID)
	require.Contains(t, allIDs, idle.ID)
}

func TestListSessionsOrdersByLastActiveAtDesc(t *testing.T) {
	svc, db, _ := newSessionServiceWithRoot(t)
	dir := t.TempDir()
	older, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: dir,
	})
	require.NoError(t, err)
	newer, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: dir,
	})
	require.NoError(t, err)
	update := db.Model(&persistence.Session{}).Where("id = ?", older.ID).
		Update("last_active_at", time.Now().Add(-time.Hour))
	require.NoError(t, update.Error)
	listed, err := svc.List(context.Background(), ListFilter{WorkspacePath: dir})
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, newer.ID, listed[0].ID)
	require.Equal(t, older.ID, listed[1].ID)
}

// sessionIDs 提取视图的 ID，使列表断言只针对自己的 fixture，
// 即使共享测试数据库中还有其他包的行也不受影响。
func sessionIDs(views []SessionView) []uuid.UUID {
	ids := make([]uuid.UUID, len(views))
	for index, view := range views {
		ids[index] = view.ID
	}
	return ids
}

func TestListSessionsSearchesTitle(t *testing.T) {
	svc, db, _ := newSessionServiceWithRoot(t)
	created, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)
	update := db.Model(&persistence.Session{}).Where("id = ?", created.ID).
		Update("title", "deploy notes")
	require.NoError(t, update.Error)
	listed, err := svc.List(context.Background(), ListFilter{Query: "deploy"})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, created.ID, listed[0].ID)
}

func TestGetSessionIncludesWorkspaceAndModelWithoutSecret(t *testing.T) {
	svc, _, _ := newSessionServiceWithRoot(t)
	dir := t.TempDir()
	created, err := svc.Create(context.Background(), CreateInput{
		ProfileID: testProfileID, Path: dir,
	})
	require.NoError(t, err)
	view, err := svc.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Workspace.Path, view.Workspace.Path)
	require.Equal(t, "integration-model", view.Model.Name)
	require.Equal(t, "test-model", view.Model.Model)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	_, err = svc.Get(context.Background(), uuid.New())
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMessagesPagesBySessionSequence(t *testing.T) {
	svc, db, _ := newSessionServiceWithRoot(t)
	created, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)
	for seq := int64(1); seq <= 5; seq++ {
		row := persistence.Message{
			ID: uuid.New(), SessionID: created.ID, SessionSeq: seq,
			Role: "user", Content: "hello", Status: "completed",
		}
		require.NoError(t, db.Create(&row).Error)
	}
	first, err := svc.Messages(context.Background(), created.ID, 0, 2)
	require.NoError(t, err)
	require.Len(t, first.Messages, 2)
	require.Equal(t, int64(1), first.Messages[0].SessionSeq)
	require.Equal(t, int64(2), first.NextAfter)
	second, err := svc.Messages(context.Background(), created.ID, first.NextAfter, 2)
	require.NoError(t, err)
	require.Len(t, second.Messages, 2)
	require.Equal(t, int64(3), second.Messages[0].SessionSeq)
	require.Equal(t, int64(4), second.NextAfter)
	last, err := svc.Messages(context.Background(), created.ID, second.NextAfter, 2)
	require.NoError(t, err)
	require.Len(t, last.Messages, 1)
	require.Zero(t, last.NextAfter)
	all, err := svc.Messages(context.Background(), created.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, all.Messages, 5)
	require.Zero(t, all.NextAfter)
	_, err = svc.Messages(context.Background(), uuid.New(), 0, 10)
	require.ErrorIs(t, err, ErrNotFound)
}

func newSessionServiceForTest(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	svc, db, _ := newSessionServiceWithRoot(t)
	return svc, db
}

func newCommittedSessionService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		t.Fatalf("%s is required for integration tests", testDatabaseURLEnv)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	t.Chdir(repoRoot)
	require.NoError(t, persistence.Migrate(databaseURL))
	db, err := persistence.Open(context.Background(), databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		require.NoError(t, sqlDB.Close())
	})
	return NewService(db, workspace.NewService(t.TempDir())), db
}

func newSessionServiceWithRoot(t *testing.T) (*Service, *gorm.DB, string) {
	t.Helper()
	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		t.Fatalf("%s is required for integration tests", testDatabaseURLEnv)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	t.Chdir(repoRoot)
	require.NoError(t, persistence.Migrate(databaseURL))
	db, err := persistence.Open(context.Background(), databaseURL)
	require.NoError(t, err)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback().Error)
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		require.NoError(t, sqlDB.Close())
	})
	profile := persistence.ModelProfile{
		ID:         testProfileID,
		Name:       "integration-model",
		BaseURL:    "http://127.0.0.1:1/v1",
		Model:      "test-model",
		SecretKind: "environment",
		SecretRef:  "TEST_API_KEY",
	}
	require.NoError(t, tx.Create(&profile).Error)
	root := t.TempDir()
	return NewService(tx, workspace.NewService(root)), tx, root
}
