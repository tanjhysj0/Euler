package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"euler/internal/logging"
)

// Limits caps a single run's lifetime.
type Limits struct {
	MaxDuration time.Duration
}

// Manager owns run workers, cancellation and graceful shutdown.
type Manager struct {
	store  *Store
	runner Runner
	limits Limits

	mu      sync.Mutex
	workers map[uuid.UUID]context.CancelFunc
	closing bool
	wg      sync.WaitGroup
}

var errManagerClosing = errors.New("run manager is shutting down")

const noDuration time.Duration = 0

// NewManager builds a Manager that persists through store. Runs are executed
// by runner under limits.
func NewManager(store *Store, runner Runner, limits Limits) *Manager {
	return &Manager{
		store:   store,
		runner:  runner,
		limits:  limits,
		workers: make(map[uuid.UUID]context.CancelFunc),
	}
}

// Start persists a new run and executes it in the background. It returns as
// soon as the run and its messages are committed.
func (m *Manager) Start(
	ctx context.Context, sessionID uuid.UUID, content string,
) (StartResult, error) {
	if m.isClosing() {
		return StartResult{}, errManagerClosing
	}
	result, err := m.store.Start(ctx, sessionID, content)
	if err != nil {
		return StartResult{}, err
	}
	request := Request{
		RunID:              result.Run.ID,
		SessionID:          sessionID,
		WorkspaceID:        result.WorkspaceID,
		AssistantMessageID: result.AssistantMessage.ID,
	}
	if !m.dispatch(ctx, request) {
		return StartResult{}, errManagerClosing
	}
	return result, nil
}

func (m *Manager) dispatch(ctx context.Context, request Request) bool {
	runCtx, cancel := m.newRunContext(ctx)
	if !m.register(request.RunID, cancel) {
		cancel()
		m.interruptRun(context.WithoutCancel(ctx), request.RunID)
		return false
	}
	go m.execute(runCtx, request)
	return true
}

// Cancel stops an active run. It is idempotent and returns nil for runs that
// are already terminal.
func (m *Manager) Cancel(ctx context.Context, runID uuid.UUID) error {
	m.mu.Lock()
	cancel := m.workers[runID]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	_, err := m.store.Cancel(ctx, runID)
	return err
}

// RecoverInterrupted repairs runs left active by a previous process.
func (m *Manager) RecoverInterrupted(ctx context.Context) error {
	return m.store.RecoverInterrupted(ctx)
}

// Shutdown cancels every worker and waits for them, bounded by ctx. Runs
// cancelled during shutdown are recorded as interrupted.
func (m *Manager) Shutdown(ctx context.Context) error {
	for _, cancel := range m.beginShutdown() {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) execute(ctx context.Context, request Request) {
	defer m.wg.Done()
	defer m.forget(request.RunID)
	defer func() {
		if recovered := recover(); recovered != nil {
			m.handlePanic(ctx, request, recovered)
		}
	}()
	dbCtx := context.WithoutCancel(ctx)
	started, err := m.store.MarkRunning(dbCtx, request)
	if err != nil {
		slog.Error("mark run running", logKeyRunID, request.RunID,
			logKeyError, logging.Redact(err.Error()))
		m.failRun(dbCtx, request.RunID)
		return
	}
	if !started {
		return
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		m.finish(dbCtx, request, ctxErr)
		return
	}
	m.finish(dbCtx, request, m.runner.Run(ctx, request))
}

// handlePanic records a worker panic as a failed run; the panic value is only
// logged after redaction and never reaches responses or events.
func (m *Manager) handlePanic(ctx context.Context, request Request, recovered any) {
	detail := logging.Redact(fmt.Sprintf("%v", recovered))
	slog.Error("run worker panic", logKeyRunID, request.RunID, logKeyError, detail)
	m.failRun(context.WithoutCancel(ctx), request.RunID)
}

func (m *Manager) finish(ctx context.Context, request Request, runErr error) {
	var coded interface{ ErrorCode() string }
	switch {
	case runErr == nil:
		m.finishRun(ctx, FinishInput{
			RunID:         request.RunID,
			Status:        StatusSucceeded,
			MessageStatus: messageCompleted,
			EventType:     eventRunCompleted,
		})
	case errors.Is(runErr, context.Canceled) && m.isClosing():
		m.interruptRun(ctx, request.RunID)
	case errors.Is(runErr, context.Canceled):
		m.cancelRun(ctx, request.RunID)
	case errors.Is(runErr, context.DeadlineExceeded):
		m.finishRun(ctx, FinishInput{
			RunID:         request.RunID,
			Status:        StatusFailed,
			MessageStatus: messageFailed,
			ErrorCode:     codeRunTimeout,
			EventType:     eventRunFailed,
		})
	case errors.As(runErr, &coded):
		slog.Error("run failed", logKeyRunID, request.RunID,
			logKeyError, logging.Redact(runErr.Error()))
		m.finishRun(ctx, FinishInput{
			RunID:         request.RunID,
			Status:        StatusFailed,
			MessageStatus: messageFailed,
			ErrorCode:     coded.ErrorCode(),
			EventType:     eventRunFailed,
		})
	default:
		slog.Error("run failed", logKeyRunID, request.RunID,
			logKeyError, logging.Redact(runErr.Error()))
		m.failRun(ctx, request.RunID)
	}
}

func (m *Manager) failRun(ctx context.Context, runID uuid.UUID) {
	m.finishRun(ctx, FinishInput{
		RunID:         runID,
		Status:        StatusFailed,
		MessageStatus: messageFailed,
		ErrorCode:     codeRunFailed,
		EventType:     eventRunFailed,
	})
}

func (m *Manager) finishRun(ctx context.Context, input FinishInput) {
	if err := m.store.Finish(ctx, input); err != nil {
		slog.Error("finish run", logKeyRunID, input.RunID,
			"status", input.Status, logKeyError, err)
	}
}

func (m *Manager) cancelRun(ctx context.Context, runID uuid.UUID) {
	if _, err := m.store.Cancel(ctx, runID); err != nil {
		slog.Error("cancel run", logKeyRunID, runID, logKeyError, err)
	}
}

func (m *Manager) interruptRun(ctx context.Context, runID uuid.UUID) {
	if err := m.store.Interrupt(ctx, runID); err != nil {
		slog.Error("interrupt run", logKeyRunID, runID, logKeyError, err)
	}
}

// newRunContext detaches the run from the HTTP request lifecycle. WithoutCancel
// strips the request's cancellation and deadline before MaxDuration adds the
// only deadline the run obeys.
func (m *Manager) newRunContext(parent context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(parent)
	if m.limits.MaxDuration > noDuration {
		return context.WithTimeout(detached, m.limits.MaxDuration)
	}
	return context.WithCancel(detached)
}

func (m *Manager) register(runID uuid.UUID, cancel context.CancelFunc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return false
	}
	m.workers[runID] = cancel
	m.wg.Add(singleWorker)
	return true
}

func (m *Manager) forget(runID uuid.UUID) {
	m.mu.Lock()
	delete(m.workers, runID)
	m.mu.Unlock()
}

func (m *Manager) beginShutdown() []context.CancelFunc {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closing = true
	cancels := make([]context.CancelFunc, noCapacity, len(m.workers))
	for _, cancel := range m.workers {
		cancels = append(cancels, cancel)
	}
	return cancels
}

func (m *Manager) isClosing() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closing
}
