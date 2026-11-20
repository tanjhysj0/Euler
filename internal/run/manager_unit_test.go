package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/persistence"
)

// unitFakeStore 用内存实现 RunStore，使 Manager 的编排逻辑无需数据库即可测试。
type unitFakeStore struct {
	mu         sync.Mutex
	startErr   error
	markRun    bool
	finishes   []FinishInput
	cancels    []uuid.UUID
	interrupts []uuid.UUID
}

func newUnitFakeStore() *unitFakeStore {
	return &unitFakeStore{markRun: true}
}

func (s *unitFakeStore) Start(
	_ context.Context, sessionID uuid.UUID, _ string,
) (StartResult, error) {
	if s.startErr != nil {
		return StartResult{}, s.startErr
	}
	runID := uuid.New()
	return StartResult{
		Snapshot:         Snapshot{Run: persistence.Run{ID: runID, SessionID: sessionID}},
		AssistantMessage: persistence.Message{ID: uuid.New()},
		WorkspaceID:      uuid.New(),
	}, nil
}

func (s *unitFakeStore) MarkRunning(context.Context, Request) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markRun, nil
}

func (s *unitFakeStore) Finish(_ context.Context, input FinishInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishes = append(s.finishes, input)
	return nil
}

func (s *unitFakeStore) Cancel(_ context.Context, runID uuid.UUID) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels = append(s.cancels, runID)
	return Snapshot{}, nil
}

func (s *unitFakeStore) Interrupt(_ context.Context, runID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupts = append(s.interrupts, runID)
	return nil
}

func (*unitFakeStore) RecoverInterrupted(context.Context) error { return nil }

func (s *unitFakeStore) finished() []FinishInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	inputs := make([]FinishInput, len(s.finishes))
	copy(inputs, s.finishes)
	return inputs
}

func (s *unitFakeStore) cancelled() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]uuid.UUID, len(s.cancels))
	copy(ids, s.cancels)
	return ids
}

func (s *unitFakeStore) interrupted() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]uuid.UUID, len(s.interrupts))
	copy(ids, s.interrupts)
	return ids
}

// unitFakeRunner 让测试控制 run 的结果与阻塞行为。
type unitFakeRunner struct {
	block bool
	fn    func(context.Context) error
}

func (r *unitFakeRunner) Run(ctx context.Context, _ Request) error {
	if r.fn != nil {
		return r.fn(ctx)
	}
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func newUnitManager(
	t *testing.T, store RunStore, runner Runner, maxDuration time.Duration,
) *Manager {
	t.Helper()
	manager := NewManager(store, runner, Limits{MaxDuration: maxDuration})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("shutdown manager: %v", err)
		}
	})
	return manager
}

func TestManagerSucceedsWithoutDatabase(t *testing.T) {
	store := newUnitFakeStore()
	manager := newUnitManager(t, store, &unitFakeRunner{}, time.Second)
	_, err := manager.Start(context.Background(), uuid.New(), "hello")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, input := range store.finished() {
			if input.Status == StatusSucceeded && input.EventType == eventRunCompleted {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond)
}

func TestManagerMapsTimeoutWithoutDatabase(t *testing.T) {
	store := newUnitFakeStore()
	manager := newUnitManager(t, store, &unitFakeRunner{block: true}, 20*time.Millisecond)
	started, err := manager.Start(context.Background(), uuid.New(), "slow")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, input := range store.finished() {
			if input.RunID == started.Run.ID && input.Status == StatusFailed &&
				input.ErrorCode == codeRunTimeout &&
				input.ErrorMessage == "run timed out" {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond)
}

func TestManagerRecordsFailureReasonWithoutDatabase(t *testing.T) {
	store := newUnitFakeStore()
	runner := &unitFakeRunner{fn: func(context.Context) error {
		return errors.New("model exploded")
	}}
	manager := newUnitManager(t, store, runner, time.Second)
	started, err := manager.Start(context.Background(), uuid.New(), "fail")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, input := range store.finished() {
			if input.RunID == started.Run.ID && input.Status == StatusFailed &&
				input.ErrorCode == codeRunFailed &&
				input.ErrorMessage == "model exploded" {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond)
}

func TestManagerRecordsCodedFailureReasonWithoutDatabase(t *testing.T) {
	store := newUnitFakeStore()
	runner := &unitFakeRunner{fn: func(context.Context) error {
		return codedMessageError{
			code: "model_round_limit",
			msg:  "model round limit exceeded",
		}
	}}
	manager := newUnitManager(t, store, runner, time.Second)
	started, err := manager.Start(context.Background(), uuid.New(), "limit")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, input := range store.finished() {
			if input.RunID == started.Run.ID && input.Status == StatusFailed &&
				input.ErrorCode == "model_round_limit" &&
				input.ErrorMessage == "model round limit exceeded" {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond)
}

func TestManagerRedactsFailureReasonWithoutDatabase(t *testing.T) {
	store := newUnitFakeStore()
	runner := &unitFakeRunner{fn: func(context.Context) error {
		return errors.New("request failed api_key=sk-secret-value")
	}}
	manager := newUnitManager(t, store, runner, time.Second)
	started, err := manager.Start(context.Background(), uuid.New(), "fail")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, input := range store.finished() {
			if input.RunID == started.Run.ID &&
				input.ErrorMessage == "request failed api_key=[REDACTED]" {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond)
}

// codedMessageError 让测试同时控制错误码与错误信息。
type codedMessageError struct {
	code string
	msg  string
}

func (e codedMessageError) Error() string     { return e.msg }
func (e codedMessageError) ErrorCode() string { return e.code }

func TestManagerCancelDelegatesToStore(t *testing.T) {
	store := newUnitFakeStore()
	manager := newUnitManager(t, store, &unitFakeRunner{block: true}, time.Minute)
	started, err := manager.Start(context.Background(), uuid.New(), "cancel me")
	require.NoError(t, err)
	require.NoError(t, manager.Cancel(context.Background(), started.Run.ID))
	require.Eventually(t, func() bool {
		for _, id := range store.cancelled() {
			if id == started.Run.ID {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond)
}

func TestManagerRecoversPanicWithoutDatabase(t *testing.T) {
	store := newUnitFakeStore()
	runner := &unitFakeRunner{fn: func(context.Context) error { panic("boom") }}
	manager := newUnitManager(t, store, runner, time.Second)
	started, err := manager.Start(context.Background(), uuid.New(), "panic")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, input := range store.finished() {
			if input.RunID == started.Run.ID && input.Status == StatusFailed &&
				input.ErrorCode == codeRunFailed && input.ErrorMessage == "boom" {
				return true
			}
		}
		return false
	}, time.Second, 5*time.Millisecond)
}

func TestManagerShutdownInterruptsActiveRun(t *testing.T) {
	store := newUnitFakeStore()
	manager := NewManager(store, &unitFakeRunner{block: true}, Limits{MaxDuration: time.Minute})
	started, err := manager.Start(context.Background(), uuid.New(), "shutdown")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, manager.Shutdown(ctx))
	require.Contains(t, store.interrupted(), started.Run.ID)
}

func TestManagerReturnsStartError(t *testing.T) {
	store := newUnitFakeStore()
	store.startErr = ErrActiveRun
	manager := newUnitManager(t, store, &unitFakeRunner{}, time.Second)
	_, err := manager.Start(context.Background(), uuid.New(), "conflict")
	require.ErrorIs(t, err, ErrActiveRun)
}

// 保证 fake 在编译期满足端口。
var _ RunStore = (*unitFakeStore)(nil)
