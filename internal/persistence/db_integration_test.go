//go:build integration

package persistence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testDatabaseURLEnv = "TEST_DATABASE_URL"

func TestMigrationAndActiveRunConstraint(t *testing.T) {
	db := openTestDB(t)
	sessionID := insertSessionFixture(t, db)
	first := Run{ID: uuid.New(), SessionID: sessionID, Status: "queued", NextEventSeq: 1}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	second := Run{ID: uuid.New(), SessionID: sessionID, Status: "running", NextEventSeq: 1}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("expected unique active run violation")
	}
}

func TestMessageSequenceConstraint(t *testing.T) {
	db := openTestDB(t)
	sessionID := insertSessionFixture(t, db)
	row := Message{
		ID:         uuid.New(),
		SessionID:  sessionID,
		SessionSeq: 1,
		Role:       "user",
		Status:     "completed",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	row.ID = uuid.New()
	if err := db.Create(&row).Error; err == nil {
		t.Fatal("expected session sequence violation")
	}
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		t.Fatalf("%s is required for integration tests", testDatabaseURLEnv)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	t.Chdir(repoRoot)
	if err = Migrate(databaseURL); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	db, err := Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
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

func insertSessionFixture(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	workspace := Workspace{ID: uuid.New(), Path: t.TempDir()}
	if err := db.Create(&workspace).Error; err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	profile := ModelProfile{
		ID:         uuid.New(),
		Name:       "integration",
		BaseURL:    "http://127.0.0.1:1/v1",
		Model:      "test-model",
		SecretKind: "environment",
		SecretRef:  "TEST_API_KEY",
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create model profile: %v", err)
	}
	session := Session{
		ID:             uuid.New(),
		WorkspaceID:    workspace.ID,
		ModelProfileID: profile.ID,
		Title:          "integration fixture",
		Status:         "active",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
		LastActiveAt:   time.Now(),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() {
		deleteFixtureRows(t, db, workspace.ID, profile.ID)
	})
	return session.ID
}

// deleteFixtureRows 删除属于单个测试工作区的所有数据行，删除遵循外键顺序。
// 绝不清空共享表。
func deleteFixtureRows(t *testing.T, db *gorm.DB, workspaceID, profileID uuid.UUID) {
	t.Helper()
	var sessionIDs []uuid.UUID
	require.NoError(t, db.Model(&Session{}).
		Where("workspace_id = ?", workspaceID).Pluck("id", &sessionIDs).Error)
	var runIDs []uuid.UUID
	if len(sessionIDs) > 0 {
		require.NoError(t, db.Model(&Run{}).
			Where("session_id IN ?", sessionIDs).Pluck("id", &runIDs).Error)
	}
	if len(runIDs) > 0 {
		require.NoError(t, db.Where("run_id IN ?", runIDs).Delete(&Approval{}).Error)
		require.NoError(t, db.Where("run_id IN ?", runIDs).Delete(&ToolCall{}).Error)
		require.NoError(t, db.Where("run_id IN ?", runIDs).Delete(&RunEvent{}).Error)
		require.NoError(t, db.Where("id IN ?", runIDs).Delete(&Run{}).Error)
	}
	if len(sessionIDs) > 0 {
		require.NoError(t, db.Where("session_id IN ?", sessionIDs).Delete(&Message{}).Error)
		require.NoError(t, db.Where("id IN ?", sessionIDs).Delete(&Session{}).Error)
	}
	require.NoError(t, db.Where("id = ?", workspaceID).Delete(&Workspace{}).Error)
	require.NoError(t, db.Where("id = ?", profileID).Delete(&ModelProfile{}).Error)
}
