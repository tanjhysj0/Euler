//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"euler/internal/approval"
	"euler/internal/event"
	"euler/internal/persistence"
	"euler/internal/workspace"
)

const testDatabaseURLEnv = "TEST_DATABASE_URL"

func TestFileReadReturnsContent(t *testing.T) {
	fixture := newToolsFixture(t)
	writeTestFile(t, fixture.dir, "note.txt", "你好 hello")
	built := fixture.buildTools(t, &fakeGate{}, 10)
	observation, err := invokeTool(t, built, "file_read", `{"path":"note.txt"}`)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(observation), &body))
	require.Equal(t, "completed", body["status"])
	require.Equal(t, "你好 hello", body["content"])
	call := fixture.lastToolCall(t, "file_read")
	require.Equal(t, "succeeded", call.Status)
	require.NotNil(t, call.FinishedAt)
	require.Equal(t, int64(1), countToolsEvents(t, fixture.db, fixture.runID, "tool.started"))
	require.Equal(t, int64(1), countToolsEvents(t, fixture.db, fixture.runID, "tool.completed"))
}

func TestFileListAndSearchReturnJSON(t *testing.T) {
	fixture := newToolsFixture(t)
	writeTestFile(t, fixture.dir, "a.txt", "a")
	writeTestFile(t, fixture.dir, "sub/b.txt", "b")
	built := fixture.buildTools(t, &fakeGate{}, 10)
	listed, err := invokeTool(t, built, "file_list", `{}`)
	require.NoError(t, err)
	require.Contains(t, listed, `"a.txt"`)
	require.Contains(t, listed, `"sub"`)
	// 默认只列根目录一层，不展开子目录内容
	require.NotContains(t, listed, `"sub/b.txt"`)

	nested, err := invokeTool(t, built, "file_list", `{"path":"sub"}`)
	require.NoError(t, err)
	require.Contains(t, nested, `"sub/b.txt"`)

	found, err := invokeTool(t, built, "file_search", `{"query":"b.txt"}`)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(found), &body))
	require.Equal(t, "completed", body["status"])
	require.Equal(t, []any{"sub/b.txt"}, body["matches"])
}

func TestFileListAndSearchRespectOutputBudget(t *testing.T) {
	fixture := newToolsFixture(t)
	long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, prefix := range []string{"a", "b", "c", "d"} {
		writeTestFile(t, fixture.dir, prefix+long+".txt", "x")
	}
	registry := NewRegistry(RegistryDeps{
		Calls: NewGormRecorder(fixture.db), Events: fixture.events, Gate: &fakeGate{},
		RunID: fixture.runID, MaxToolCalls: 10, MaxOutputBytes: 130,
		APIKey: "sk-fixture-key",
	})
	built, err := registry.Tools(
		workspace.NewFileStore(fixture.dir, 1024),
		NewShellExecutor(ShellConfig{Timeout: time.Second, MaxOutputBytes: 1024}),
		fixture.dir, nil)
	require.NoError(t, err)
	for _, call := range []struct{ name, args, key string }{
		{"file_list", `{}`, "entries"},
		{"file_search", `{"query":".txt"}`, "matches"},
	} {
		observation, invokeErr := invokeTool(t, built, call.name, call.args)
		require.NoError(t, invokeErr)
		require.LessOrEqual(t, len(observation), 130)
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(observation), &body))
		require.Equal(t, "completed", body["status"])
		require.Equal(t, true, body["truncated"])
		require.NotEmpty(t, body[call.key])
	}
}

func TestFileReadBinaryContentKeepsJSONBValid(t *testing.T) {
	fixture := newToolsFixture(t)
	writeTestFile(t, fixture.dir, "bin.dat", "a\x00b")
	built := fixture.buildTools(t, &fakeGate{}, 10)
	observation, err := invokeTool(t, built, "file_read", `{"path":"bin.dat"}`)
	require.NoError(t, err)
	require.NotContains(t, observation, "\\u0000")
	call := fixture.lastToolCall(t, "file_read")
	require.Equal(t, "succeeded", call.Status)
}

func TestFileWriteRejectedLeavesFileUnchanged(t *testing.T) {
	fixture := newToolsFixture(t)
	writeTestFile(t, fixture.dir, "out.txt", "old")
	gate := &fakeGate{decision: approval.Decision{Status: approval.StatusRejected}}
	built := fixture.buildTools(t, gate, 10)
	observation, err := invokeTool(t, built, "file_write",
		`{"path":"out.txt","content":"new"}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"status":"rejected","message":"用户拒绝"}`, observation)
	require.Equal(t, "old", readTestFile(t, fixture.dir, "out.txt"))
	require.Len(t, gate.requests, 1)
	require.Equal(t, "out.txt", gate.requests[0].Payload["path"])
	require.NotEmpty(t, gate.requests[0].Payload["diff"])
	call := fixture.lastToolCall(t, "file_write")
	require.JSONEq(t, observation, string(call.ResultJSON))
}

func TestFileWriteApprovedAppliesDiff(t *testing.T) {
	fixture := newToolsFixture(t)
	gate := &fakeGate{decision: approval.Decision{
		Approved: true, Status: approval.StatusApproved,
	}}
	built := fixture.buildTools(t, gate, 10)
	observation, err := invokeTool(t, built, "file_write",
		`{"path":"new.txt","content":"fresh"}`)
	require.NoError(t, err)
	require.Contains(t, observation, `"status":"completed"`)
	require.Equal(t, "fresh", readTestFile(t, fixture.dir, "new.txt"))
	require.NotEmpty(t, gate.requests[0].Payload["diff"])
	require.Equal(t, "succeeded", fixture.lastToolCall(t, "file_write").Status)
}

func TestFilePatchAndDeleteApproved(t *testing.T) {
	fixture := newToolsFixture(t)
	writeTestFile(t, fixture.dir, "patch.txt", "hello world")
	gate := &fakeGate{decision: approval.Decision{
		Approved: true, Status: approval.StatusApproved,
	}}
	built := fixture.buildTools(t, gate, 10)
	_, err := invokeTool(t, built, "file_patch",
		`{"path":"patch.txt","old_string":"world","new_string":"euler"}`)
	require.NoError(t, err)
	require.Equal(t, "hello euler", readTestFile(t, fixture.dir, "patch.txt"))
	_, err = invokeTool(t, built, "file_delete", `{"path":"patch.txt"}`)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(fixture.dir, "patch.txt"))
	require.True(t, os.IsNotExist(statErr))
}

func TestRecoverableErrorsReturnFailedObservation(t *testing.T) {
	fixture := newToolsFixture(t)
	built := fixture.buildTools(t, &fakeGate{}, 10)
	observation, err := invokeTool(t, built, "file_read", `{"path":"note.txt"}`)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"status":"failed","error":{"code":"file_not_found","message":"文件不存在：note.txt"}}`,
		observation)
	call := fixture.lastToolCall(t, "file_read")
	require.Equal(t, "failed", call.Status)
	require.Equal(t, int64(1), countToolsEvents(t, fixture.db, fixture.runID, "tool.failed"))
	observation, err = invokeTool(t, built, "file_read", `{"path":"../escape.txt"}`)
	require.NoError(t, err)
	require.Contains(t, observation, `"code":"path_outside_workspace"`)
	writeTestFile(t, fixture.dir, "dup.txt", "x x")
	observation, err = invokeTool(t, built, "file_patch",
		`{"path":"dup.txt","old_string":"x","new_string":"y"}`)
	require.NoError(t, err)
	require.Contains(t, observation, `"code":"patch_ambiguous"`)
}

func TestToolCallBudgetAbortsRun(t *testing.T) {
	fixture := newToolsFixture(t)
	built := fixture.buildTools(t, &fakeGate{}, 1)
	_, err := invokeTool(t, built, "file_list", `{}`)
	require.NoError(t, err)
	_, err = invokeTool(t, built, "file_list", `{}`)
	require.ErrorIs(t, err, ErrToolCallLimit)
	var coded *CodedError
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "tool_call_limit", coded.ErrorCode())
	var calls []persistence.ToolCall
	require.NoError(t, fixture.db.Where("run_id = ?", fixture.runID).
		Order("created_at ASC").Find(&calls).Error)
	require.Len(t, calls, 2)
	require.Equal(t, "failed", calls[1].Status)
}

func TestShellApprovedExecutesAndRejectedDoesNot(t *testing.T) {
	fixture := newToolsFixture(t)
	approved := &fakeGate{decision: approval.Decision{
		Approved: true, Status: approval.StatusApproved,
	}}
	built := fixture.buildTools(t, approved, 10)
	observation, err := invokeTool(t, built, "shell", `{"command":"echo hi"}`)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(observation), &body))
	require.Equal(t, "completed", body["status"])
	require.Contains(t, body["output"], "hi")
	require.Equal(t, "echo hi", approved.requests[0].Payload["command"])
	rejected := &fakeGate{decision: approval.Decision{Status: approval.StatusRejected}}
	built = fixture.buildTools(t, rejected, 10)
	observation, err = invokeTool(t, built, "shell", `{"command":"touch side.txt"}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"status":"rejected","message":"用户拒绝"}`, observation)
	_, statErr := os.Stat(filepath.Join(fixture.dir, "side.txt"))
	require.True(t, os.IsNotExist(statErr))
}

func TestObservationRedactsAPIKey(t *testing.T) {
	fixture := newToolsFixture(t)
	writeTestFile(t, fixture.dir, "secret.txt", "token sk-fixture-key end")
	built := fixture.buildTools(t, &fakeGate{}, 10)
	observation, err := invokeTool(t, built, "file_read", `{"path":"secret.txt"}`)
	require.NoError(t, err)
	require.NotContains(t, observation, "sk-fixture-key")
	require.Contains(t, observation, "[REDACTED]")
	call := fixture.lastToolCall(t, "file_read")
	require.NotContains(t, string(call.ResultJSON), "sk-fixture-key")
}

type toolsFixture struct {
	db     *gorm.DB
	events *event.Store
	runID  uuid.UUID
	dir    string
}

func newToolsFixture(t *testing.T) *toolsFixture {
	t.Helper()
	db := openToolsTestDB(t)
	dir := t.TempDir()
	now := time.Now().UTC()
	workspaceRow := persistence.Workspace{ID: uuid.New(), Path: dir, CreatedAt: now}
	require.NoError(t, db.Create(&workspaceRow).Error)
	profile := persistence.ModelProfile{
		ID: uuid.New(), Name: "tools integration", BaseURL: "http://127.0.0.1:1/v1",
		Model: "test-model", SecretKind: "environment", SecretRef: "TEST_API_KEY",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&profile).Error)
	session := persistence.Session{
		ID: uuid.New(), WorkspaceID: workspaceRow.ID, ModelProfileID: profile.ID,
		Title: "tools fixture", Status: "active",
		CreatedAt: now, UpdatedAt: now, LastActiveAt: now,
	}
	require.NoError(t, db.Create(&session).Error)
	runRow := persistence.Run{
		ID: uuid.New(), SessionID: session.ID, Status: "running",
		NextEventSeq: 1, CreatedAt: now, StartedAt: &now,
	}
	require.NoError(t, db.Create(&runRow).Error)
	t.Cleanup(func() {
		deleteToolsFixtures(t, db, workspaceRow.ID, profile.ID)
	})
	return &toolsFixture{
		db: db, events: event.NewStore(db, event.NewHub()), runID: runRow.ID, dir: dir,
	}
}

func (f *toolsFixture) buildTools(
	t *testing.T, gate approval.Gate, maxCalls int,
) []tool.BaseTool {
	t.Helper()
	registry := NewRegistry(RegistryDeps{
		Calls: NewGormRecorder(f.db), Events: f.events, Gate: gate, RunID: f.runID,
		MaxToolCalls: maxCalls, MaxOutputBytes: 64 * 1024, APIKey: "sk-fixture-key",
	})
	built, err := registry.Tools(
		workspace.NewFileStore(f.dir, 1024*1024),
		NewShellExecutor(ShellConfig{Timeout: 5 * time.Second, MaxOutputBytes: 1024}),
		f.dir, []string{"EULER_TEST_SECRET"},
	)
	require.NoError(t, err)
	return built
}

func (f *toolsFixture) lastToolCall(t *testing.T, name string) persistence.ToolCall {
	t.Helper()
	var call persistence.ToolCall
	require.NoError(t, f.db.Where("run_id = ? AND name = ?", f.runID, name).
		Order("created_at DESC").First(&call).Error)
	return call
}

func openToolsTestDB(t *testing.T) *gorm.DB {
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

// deleteToolsFixtures 只按外键顺序删除单个 fixture 工作区拥有的行，
// 绝不截断共享表。
func deleteToolsFixtures(t *testing.T, db *gorm.DB, workspaceID, profileID uuid.UUID) {
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

func countToolsEvents(t *testing.T, db *gorm.DB, runID uuid.UUID, eventType string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&persistence.RunEvent{}).
		Where("run_id = ? AND type = ?", runID, eventType).Count(&count).Error)
	return count
}

func invokeTool(t *testing.T, built []tool.BaseTool, name, args string) (string, error) {
	t.Helper()
	for _, candidate := range built {
		info, err := candidate.Info(context.Background())
		require.NoError(t, err)
		if info.Name != name {
			continue
		}
		invokable, ok := candidate.(tool.InvokableTool)
		require.True(t, ok)
		return invokable.InvokableRun(context.Background(), args)
	}
	t.Fatalf("tool %s not registered", name)
	return "", errors.New("tool not registered")
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
}

func readTestFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(data)
}
