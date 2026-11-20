//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"euler/internal/approval"
	"euler/internal/config"
	"euler/internal/event"
	"euler/internal/modelprofile"
	"euler/internal/persistence"
	"euler/internal/run"
	"euler/internal/tools"
	"euler/internal/workspace"
)

const testDatabaseURLEnv = "TEST_DATABASE_URL"

func TestRuntimeStreamsTextBeforeToolCall(t *testing.T) {
	fixture := newRuntimeFixture(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(fixture.dir, "note.txt"), []byte("你好"), 0o600))
	index := 0
	// OpenAI 在第一个非空 delta 中声明 tool_calls；文本和工具调用
	// 会在同一个 chunk 中到达。
	chatModel := &fakeReactModel{rounds: [][]*schema.Message{
		{
			{Role: schema.Assistant, Content: "先检查", ToolCalls: []schema.ToolCall{{
				Index: &index, ID: "call_1", Type: "function",
				Function: schema.FunctionCall{
					Name: "file_read", Arguments: `{"path":"note.txt"}`,
				},
			}}},
		},
		{schema.AssistantMessage("完成", nil)},
	}}
	runtime := NewRuntime(fixture.deps(t, chatModel))
	require.NoError(t, runtime.Run(context.Background(), fixture.request))
	var message persistence.Message
	require.NoError(t, fixture.db.First(&message,
		"id = ?", fixture.request.AssistantMessageID).Error)
	require.Equal(t, "先检查完成", message.Content)
	var calls []persistence.ToolCall
	require.NoError(t, fixture.db.Where("run_id = ?", fixture.request.RunID).
		Find(&calls).Error)
	require.Len(t, calls, 1)
	require.Equal(t, "file_read", calls[0].Name)
	require.Equal(t, "succeeded", calls[0].Status)
	require.Equal(t, int64(1), countAgentEvents(t, fixture.db, fixture.request.RunID,
		"tool.started"))
	require.Equal(t, int64(1), countAgentEvents(t, fixture.db, fixture.request.RunID,
		"tool.completed"))
	deltas := agentEventPayloads(t, fixture.db, fixture.request.RunID, "message.delta")
	require.NotEmpty(t, deltas)
	require.Contains(t, deltas[0]["delta"], "先检查")
}

// TestRuntimeStreamsSlowTextBeforeEOF 要求在模型 stream 尚未关闭时就能收到
// message.delta 事件；等待 EOF 才检查的做法会让慢速 stream 卡住。
func TestRuntimeStreamsSlowTextBeforeEOF(t *testing.T) {
	fixture := newRuntimeFixture(t)
	release := make(chan struct{})
	runtime := NewRuntime(fixture.deps(t, &slowFakeModel{release: release}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- runtime.Run(ctx, fixture.request) }()
	deadline := time.Now().Add(5 * time.Second)
	for countAgentEvents(t, fixture.db, fixture.request.RunID, "message.delta") == 0 {
		if time.Now().After(deadline) {
			cancel()
			<-errCh
			t.Fatal("no message.delta before model stream EOF")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	require.NoError(t, <-errCh)
}

// slowFakeModel 像慢速的 OpenAI SSE stream 一样发送小 chunk，然后阻塞直到 release。
type slowFakeModel struct{ release chan struct{} }

func (m *slowFakeModel) Stream(
	ctx context.Context, _ []*schema.Message, _ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	reader, pipe := schema.Pipe[*schema.Message](1)
	go func() {
		defer pipe.Close()
		for i := 0; i < 30; i++ {
			chunk := schema.AssistantMessage(fmt.Sprintf("片段[%04d] ", i), nil)
			if closed := pipe.Send(chunk, nil); closed {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		select {
		case <-m.release:
		case <-ctx.Done():
		}
	}()
	return reader, nil
}

func (*slowFakeModel) Generate(context.Context, []*schema.Message, ...model.Option,
) (*schema.Message, error) {
	return nil, errors.New("generate not supported")
}

func (m *slowFakeModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestRuntimeCancelStopsModel(t *testing.T) {
	fixture := newRuntimeFixture(t)
	chatModel := &fakeReactModel{block: true, started: make(chan struct{})}
	runtime := NewRuntime(fixture.deps(t, chatModel))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Run(ctx, fixture.request)
	}()
	select {
	case <-chatModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model stream never started")
	}
	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
}

// TestConsumeRoundMapsStreamError 向 future 路径的消费者注入原始的模型
// stream 错误（即 retryModel 包装图侧 reader 之前 acl 交给 MessageFuture 的
// 错误形态），并要求返回带错误码且已脱敏的错误。
func TestConsumeRoundMapsStreamError(t *testing.T) {
	fixture := newRuntimeFixture(t)
	writer := newDeltaWriter(
		fixture.events, fixture.request, runtimeNoopMessages{}, "sk-stream-test",
	)
	reader, pipe := schema.Pipe[*schema.Message](2)
	pipe.Send(schema.AssistantMessage("partial", nil), nil)
	pipe.Send(nil, errors.New("upstream failed for key sk-stream-test"))
	pipe.Close()
	err := consumeRound(reader, writer)
	require.Error(t, err)
	var coded *tools.CodedError
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "model_protocol", coded.ErrorCode())
	require.NotContains(t, err.Error(), "sk-stream-test")
	require.Contains(t, err.Error(), "[REDACTED]")
}

func TestConsumeRoundPassesContextError(t *testing.T) {
	fixture := newRuntimeFixture(t)
	writer := newDeltaWriter(
		fixture.events, fixture.request, runtimeNoopMessages{}, "sk-stream-test",
	)
	reader, pipe := schema.Pipe[*schema.Message](1)
	pipe.Send(nil, context.Canceled)
	pipe.Close()
	err := consumeRound(reader, writer)
	require.ErrorIs(t, err, context.Canceled)
}

type fakeReactModel struct {
	mu       sync.Mutex
	calls    int
	rounds   [][]*schema.Message
	block    bool
	started  chan struct{}
	notified bool
}

func (f *fakeReactModel) Stream(
	ctx context.Context, _ []*schema.Message, _ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	f.mu.Lock()
	round := f.calls
	f.calls++
	if f.block && !f.notified {
		f.notified = true
		close(f.started)
	}
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if round >= len(f.rounds) {
		return nil, errors.New("unexpected model round")
	}
	return schema.StreamReaderFromArray(f.rounds[round]), nil
}

func (f *fakeReactModel) Generate(
	_ context.Context, _ []*schema.Message, _ ...model.Option,
) (*schema.Message, error) {
	f.mu.Lock()
	round := f.calls
	f.calls++
	f.mu.Unlock()
	if round >= len(f.rounds) {
		return nil, errors.New("unexpected model round")
	}
	return schema.ConcatMessages(f.rounds[round])
}

func (f *fakeReactModel) WithTools(
	_ []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	return f, nil
}

type staticResolver struct{ profile modelprofile.ResolvedProfile }

func (r staticResolver) Resolve(
	context.Context, uuid.UUID,
) (modelprofile.ResolvedProfile, error) {
	return r.profile, nil
}

func (staticResolver) EnvironmentSecretNames(context.Context) ([]string, error) {
	return nil, nil
}

type runtimeGate struct{}

func (runtimeGate) Require(context.Context, approval.Request) (approval.Decision, error) {
	return approval.Decision{Approved: true, Status: approval.StatusApproved}, nil
}

type runtimeFixture struct {
	db      *gorm.DB
	events  *event.Store
	request run.Request
	dir     string
}

func (f *runtimeFixture) deps(t *testing.T, chatModel model.ToolCallingChatModel) Deps {
	t.Helper()
	resolver := staticResolver{profile: modelprofile.ResolvedProfile{
		ID: uuid.New(), BaseURL: "http://127.0.0.1:1/v1",
		Model: "test-model", APIKey: "sk-runtime-test",
	}}
	return Deps{
		Loader: NewDBLoader(f.db, resolver, workspace.DBWorkspaceFinder{DB: f.db}, ""),
		Events: f.events, Gate: runtimeGate{},
		Factory: fakeFactory{chatModel: chatModel},
		Limits: config.AgentLimits{
			MaxModelRounds: 4, MaxToolCalls: 8, MaxOutputBytes: 4096,
		},
		ShellTimeout: 5 * time.Second, ShellOutputBytes: 1024, PreviewBytes: 4096,
	}
}

func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	db := openAgentTestDB(t)
	dir := t.TempDir()
	now := time.Now().UTC()
	workspaceRow := persistence.Workspace{ID: uuid.New(), Path: dir, CreatedAt: now}
	require.NoError(t, db.Create(&workspaceRow).Error)
	profile := persistence.ModelProfile{
		ID: uuid.New(), Name: "agent integration", BaseURL: "http://127.0.0.1:1/v1",
		Model: "test-model", SecretKind: "environment", SecretRef: "TEST_API_KEY",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&profile).Error)
	session := persistence.Session{
		ID: uuid.New(), WorkspaceID: workspaceRow.ID, ModelProfileID: profile.ID,
		Title: "agent fixture", Status: "active",
		CreatedAt: now, UpdatedAt: now, LastActiveAt: now,
	}
	require.NoError(t, db.Create(&session).Error)
	runRow := persistence.Run{
		ID: uuid.New(), SessionID: session.ID, Status: "running",
		NextEventSeq: 1, CreatedAt: now, StartedAt: &now,
	}
	require.NoError(t, db.Create(&runRow).Error)
	userMessage := persistence.Message{
		ID: uuid.New(), SessionID: session.ID, RunID: &runRow.ID, Role: "user",
		SessionSeq: 1, Content: "读一下 note.txt", Status: "completed",
		CreatedAt: now, CompletedAt: &now,
	}
	require.NoError(t, db.Create(&userMessage).Error)
	assistant := persistence.Message{
		ID: uuid.New(), SessionID: session.ID, RunID: &runRow.ID, Role: "assistant",
		SessionSeq: 2, Content: "", Status: "streaming", CreatedAt: now,
	}
	require.NoError(t, db.Create(&assistant).Error)
	t.Cleanup(func() {
		deleteAgentFixtures(t, db, workspaceRow.ID, profile.ID)
	})
	return &runtimeFixture{
		db: db, events: event.NewStore(db, event.NewHub()), dir: dir,
		request: run.Request{
			RunID: runRow.ID, SessionID: session.ID, WorkspaceID: workspaceRow.ID,
			AssistantMessageID: assistant.ID,
		},
	}
}

func openAgentTestDB(t *testing.T) *gorm.DB {
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

// deleteAgentFixtures 只删除属于单个 fixture 工作区的行，按外键顺序删除，
// 绝不清空共享表。
func deleteAgentFixtures(t *testing.T, db *gorm.DB, workspaceID, profileID uuid.UUID) {
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

func countAgentEvents(t *testing.T, db *gorm.DB, runID uuid.UUID, eventType string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&persistence.RunEvent{}).
		Where("run_id = ? AND type = ?", runID, eventType).Count(&count).Error)
	return count
}

func agentEventPayloads(
	t *testing.T, db *gorm.DB, runID uuid.UUID, eventType string,
) []map[string]any {
	t.Helper()
	var rows []persistence.RunEvent
	require.NoError(t, db.Where("run_id = ? AND type = ?", runID, eventType).
		Order("seq ASC").Find(&rows).Error)
	payloads := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var payload map[string]any
		require.NoError(t, json.Unmarshal(row.PayloadJSON, &payload))
		payloads = append(payloads, payload)
	}
	return payloads
}
