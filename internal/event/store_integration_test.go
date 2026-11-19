//go:build integration

package event

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

const testDatabaseURLEnv = "TEST_DATABASE_URL"

func TestAppendIncrementsSequenceBeforePublish(t *testing.T) {
	db := openTestDB(t)
	runID := insertRunFixture(t, db)
	hub := NewHub()
	store := NewStore(db, hub)
	events, cancel := store.Subscribe(runID)
	defer cancel()
	first, err := store.Append(
		context.Background(), runID, "run.started", map[string]any{"ok": true},
	)
	require.NoError(t, err)
	second, err := store.Append(
		context.Background(), runID, "message.started", map[string]any{"id": "m1"},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Seq)
	require.Equal(t, int64(2), second.Seq)
	var count int64
	eventQuery := db.Model(&persistence.RunEvent{}).Where("run_id = ?", runID)
	require.NoError(t, eventQuery.Count(&count).Error)
	require.Equal(t, int64(2), count)
	require.Equal(t, first.Seq, (<-events).Seq)
	require.Equal(t, second.Seq, (<-events).Seq)
}

func TestAppendConcurrentSequences(t *testing.T) {
	db := openTestDB(t)
	runID := insertRunFixture(t, db)
	store := NewStore(db, NewHub())
	const total = 20
	sequences := make(chan int64, total)
	failures := make(chan error, total)
	var group sync.WaitGroup
	for index := 0; index < total; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			item, err := store.Append(
				context.Background(), runID, "message.delta", map[string]any{"i": 1},
			)
			if err != nil {
				failures <- err
				return
			}
			sequences <- item.Seq
		}()
	}
	group.Wait()
	close(sequences)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	collected := make([]int64, 0, total)
	for seq := range sequences {
		collected = append(collected, seq)
	}
	require.Len(t, collected, total)
	slices.Sort(collected)
	for index, seq := range collected {
		require.Equal(t, int64(index+1), seq)
	}
	var count int64
	eventQuery := db.Model(&persistence.RunEvent{}).Where("run_id = ?", runID)
	require.NoError(t, eventQuery.Count(&count).Error)
	require.Equal(t, int64(total), count)
}

func TestAppendUnknownRun(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db, NewHub())
	_, err := store.Append(context.Background(), uuid.New(), "run.started", map[string]any{})
	require.ErrorIs(t, err, ErrRunNotFound)
}

func TestListAfterOrdersBySequence(t *testing.T) {
	db := openTestDB(t)
	runID := insertRunFixture(t, db)
	store := NewStore(db, NewHub())
	for _, eventType := range []string{"message.started", "message.delta", "message.completed"} {
		_, err := store.Append(
			context.Background(), runID, eventType, map[string]any{"type": eventType},
		)
		require.NoError(t, err)
	}
	events, err := store.ListAfter(context.Background(), runID, 1)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, int64(2), events[0].Seq)
	require.Equal(t, int64(3), events[1].Seq)
	require.JSONEq(t, `{"type":"message.completed"}`, string(events[1].Payload))
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
	if err = persistence.Migrate(databaseURL); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	db, err := persistence.Open(context.Background(), databaseURL)
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

func insertRunFixture(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	workspace := persistence.Workspace{ID: uuid.New(), Path: t.TempDir()}
	require.NoError(t, db.Create(&workspace).Error)
	profile := persistence.ModelProfile{
		ID:         uuid.New(),
		Name:       "event integration",
		BaseURL:    "http://127.0.0.1:1/v1",
		Model:      "test-model",
		SecretKind: "environment",
		SecretRef:  "TEST_API_KEY",
	}
	require.NoError(t, db.Create(&profile).Error)
	now := time.Now()
	session := persistence.Session{
		ID:             uuid.New(),
		WorkspaceID:    workspace.ID,
		ModelProfileID: profile.ID,
		Title:          "event fixture",
		Status:         "active",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActiveAt:   now,
	}
	require.NoError(t, db.Create(&session).Error)
	run := persistence.Run{ID: uuid.New(), SessionID: session.ID, Status: "queued", NextEventSeq: 1}
	require.NoError(t, db.Create(&run).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Where("run_id = ?", run.ID).
			Delete(&persistence.Approval{}).Error)
		require.NoError(t, db.Where("run_id = ?", run.ID).
			Delete(&persistence.ToolCall{}).Error)
		require.NoError(t, db.Where("run_id = ?", run.ID).
			Delete(&persistence.RunEvent{}).Error)
		require.NoError(t, db.Where("session_id = ?", session.ID).
			Delete(&persistence.Message{}).Error)
		require.NoError(t, db.Where("id = ?", run.ID).Delete(&persistence.Run{}).Error)
		require.NoError(t, db.Where("id = ?", session.ID).
			Delete(&persistence.Session{}).Error)
		require.NoError(t, db.Where("id = ?", workspace.ID).
			Delete(&persistence.Workspace{}).Error)
		require.NoError(t, db.Where("id = ?", profile.ID).
			Delete(&persistence.ModelProfile{}).Error)
	})
	return run.ID
}
