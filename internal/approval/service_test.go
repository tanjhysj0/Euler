package approval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/event"
)

// fakeStore 是内存版 Store，遵循与 DBStore 相同的“首次决策生效”约定，
// 使服务测试无需数据库即可验证等待逻辑。
type fakeStore struct {
	mu         sync.Mutex
	records    map[uuid.UUID]Record
	openErr    error
	resolveErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{records: make(map[uuid.UUID]Record)}
}

func (f *fakeStore) Open(_ context.Context, request Request) (event.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return event.Event{}, f.openErr
	}
	f.records[request.ID] = Record{
		ID:          request.ID,
		RunID:       request.RunID,
		ToolCallID:  request.ToolCallID,
		Status:      StatusPending,
		RequestedAt: time.Now().UTC(),
	}
	return event.Event{RunID: request.RunID, Seq: 1, Type: EventRequested}, nil
}

func (f *fakeStore) Resolve(
	_ context.Context,
	id uuid.UUID,
	resolution Resolution,
) (Record, event.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveErr != nil {
		return Record{}, event.Event{}, f.resolveErr
	}
	record, ok := f.records[id]
	if !ok {
		return Record{}, event.Event{}, ErrNotFound
	}
	if record.Status != StatusPending {
		return record, event.Event{}, ErrAlreadyDecided
	}
	now := time.Now().UTC()
	record.Status = resolution.Status
	record.DecidedAt = &now
	record.Decision = Decision{
		Approved: resolution.Approved,
		Status:   resolution.Status,
		Reason:   resolution.Reason,
	}
	f.records[id] = record
	return record, event.Event{RunID: record.RunID, Seq: 2, Type: EventResolved}, nil
}

func (f *fakeStore) Get(_ context.Context, id uuid.UUID) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.records[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func (f *fakeStore) pendingFor(toolCallID uuid.UUID) (uuid.UUID, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, record := range f.records {
		if record.ToolCallID == toolCallID && record.Status == StatusPending {
			return id, true
		}
	}
	return uuid.Nil, false
}

// fakeBroadcaster 按顺序记录每个已发布的事件。
type fakeBroadcaster struct {
	mu     sync.Mutex
	events []event.Event
}

func (f *fakeBroadcaster) Publish(committed event.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, committed)
}

func (f *fakeBroadcaster) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	types := make([]string, len(f.events))
	for index, published := range f.events {
		types[index] = published.Type
	}
	return types
}

type serviceFixture struct {
	service    *Service
	store      *fakeStore
	events     *fakeBroadcaster
	runID      uuid.UUID
	toolCallID uuid.UUID
}

func newServiceFixture() *serviceFixture {
	store := newFakeStore()
	events := &fakeBroadcaster{}
	return &serviceFixture{
		service:    NewService(store, events),
		store:      store,
		events:     events,
		runID:      uuid.New(),
		toolCallID: uuid.New(),
	}
}

func (f *serviceFixture) request() Request {
	return Request{
		RunID:      f.runID,
		ToolCallID: f.toolCallID,
		Name:       "shell",
		Payload:    map[string]any{"command": "pwd"},
	}
}

// waitForPendingApproval 轮询存储，直到 Require 持久化了 pending 行，
// 然后返回其真实 id。断言保留在测试 goroutine 中执行。
func (f *serviceFixture) waitForPendingApproval(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.Eventually(t, func() bool {
		found, ok := f.store.pendingFor(f.toolCallID)
		id = found
		return ok
	}, time.Second, 5*time.Millisecond)
	return id
}

type requireResult struct {
	decision Decision
	err      error
}

func (f *serviceFixture) startRequire(ctx context.Context) <-chan requireResult {
	result := make(chan requireResult, 1)
	go func() {
		decision, err := f.service.Require(ctx, f.request())
		result <- requireResult{decision: decision, err: err}
	}()
	return result
}

func TestApprovalBlocksUntilDecision(t *testing.T) {
	fixture := newServiceFixture()
	result := fixture.startRequire(context.Background())
	approvalID := fixture.waitForPendingApproval(t)
	require.Never(t, func() bool { return len(result) > 0 },
		100*time.Millisecond, 10*time.Millisecond)
	_, err := fixture.service.Decide(context.Background(), approvalID,
		Resolution{Status: StatusApproved, Approved: true, Reason: "ok"})
	require.NoError(t, err)
	got := <-result
	require.NoError(t, got.err)
	require.True(t, got.decision.Approved)
	require.Equal(t, StatusApproved, got.decision.Status)
	_, err = fixture.service.Decide(context.Background(), approvalID,
		Resolution{Status: StatusRejected})
	require.ErrorIs(t, err, ErrAlreadyDecided)
	require.Equal(t, []string{EventRequested, EventResolved}, fixture.events.types())
}

func TestApprovalRejectionReturnsNotApproved(t *testing.T) {
	fixture := newServiceFixture()
	result := fixture.startRequire(context.Background())
	approvalID := fixture.waitForPendingApproval(t)
	_, err := fixture.service.Decide(context.Background(), approvalID,
		Resolution{Status: StatusRejected, Reason: "当前目录不允许执行该命令"})
	require.NoError(t, err)
	got := <-result
	require.NoError(t, got.err)
	require.False(t, got.decision.Approved)
	require.Equal(t, StatusRejected, got.decision.Status)
	require.Equal(t, "当前目录不允许执行该命令", got.decision.Reason)
}

func TestApprovalCancelCancelsApproval(t *testing.T) {
	fixture := newServiceFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fixture.startRequire(ctx)
	approvalID := fixture.waitForPendingApproval(t)
	cancel()
	got := <-result
	require.ErrorIs(t, got.err, context.Canceled)
	require.Equal(t, StatusCancelled, got.decision.Status)
	record, err := fixture.store.Get(context.Background(), approvalID)
	require.NoError(t, err)
	require.Equal(t, StatusCancelled, record.Status)
	require.Equal(t, []string{EventRequested, EventResolved}, fixture.events.types())
}

func TestApprovalCancelReturnsDecisionThatWonRace(t *testing.T) {
	fixture := newServiceFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := fixture.startRequire(ctx)
	approvalID := fixture.waitForPendingApproval(t)
	// 模拟另一个决策者已提交的决策：abandon 执行时，存储会返回
	// ErrAlreadyDecided 并附带已持久化的决策。
	fixture.store.mu.Lock()
	record := fixture.store.records[approvalID]
	record.Status = StatusApproved
	record.Decision = Decision{Approved: true, Status: StatusApproved}
	fixture.store.records[approvalID] = record
	fixture.store.mu.Unlock()
	cancel()
	got := <-result
	require.NoError(t, got.err)
	require.True(t, got.decision.Approved)
}

func TestRequireRejectsInvalidRequest(t *testing.T) {
	fixture := newServiceFixture()
	cases := map[string]Request{
		"missing run":       {ToolCallID: fixture.toolCallID, Name: "shell"},
		"missing tool call": {RunID: fixture.runID, Name: "shell"},
		"missing name":      {RunID: fixture.runID, ToolCallID: fixture.toolCallID},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := fixture.service.Require(context.Background(), request)
			require.ErrorIs(t, err, ErrInvalidRequest)
		})
	}
	require.Empty(t, fixture.store.records)
	require.Empty(t, fixture.events.types())
}

func TestRequireOpenFailureRemovesWaiter(t *testing.T) {
	fixture := newServiceFixture()
	fixture.store.openErr = errors.New("database down")
	_, err := fixture.service.Require(context.Background(), fixture.request())
	require.Error(t, err)
	fixture.service.mu.Lock()
	require.Empty(t, fixture.service.waiters)
	fixture.service.mu.Unlock()
	require.Empty(t, fixture.events.types())
}
