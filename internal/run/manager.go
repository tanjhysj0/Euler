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
	"euler/internal/tracing"
)

// Limits 限制单个 run 的生命周期。
type Limits struct {
	MaxDuration time.Duration
}

// Tracer 负责 run 级 trace 的建立与收尾。实现可为 nil，此时关闭追踪。
type Tracer interface {
	StartRun(ctx context.Context, meta tracing.TraceMeta) context.Context
	StartSpan(ctx context.Context, name string) (context.Context, func(error))
	FinishRun(ctx context.Context, errMsg string)
}

// TitleNamer 在 run 成功后为新会话生成可读标题。实现可为 nil，此时跳过。
type TitleNamer interface {
	NameSession(ctx context.Context, sessionID uuid.UUID) error
}

// Deps 汇总 Manager 的可选协作组件，避免构造函数参数继续膨胀。
type Deps struct {
	Tracer Tracer
	Titles TitleNamer
}

// Manager 负责 run worker、取消和优雅关闭。
type Manager struct {
	store  RunStore
	runner Runner
	limits Limits
	tracer Tracer
	titles TitleNamer

	mu      sync.Mutex
	workers map[uuid.UUID]context.CancelFunc
	closing bool
	wg      sync.WaitGroup
}

var errManagerClosing = errors.New("run manager is shutting down")

const (
	noDuration time.Duration = 0
	// spanMarkRunning 是 run 进入执行状态时写入数据库的子观测名。
	spanMarkRunning = "mark-running"
)

// NewManager 构建一个通过 store 持久化的 Manager。run 由 runner 在 limits
// 约束下执行，deps 为 nil 时关闭追踪与标题生成。
func NewManager(store RunStore, runner Runner, limits Limits, deps Deps) *Manager {
	return &Manager{
		store:   store,
		runner:  runner,
		limits:  limits,
		tracer:  deps.Tracer,
		titles:  deps.Titles,
		workers: make(map[uuid.UUID]context.CancelFunc),
	}
}

// Start 持久化一个新的 run 并在后台执行它。run 及其消息提交完成后即返回。
// trace 在分发前建立，使排队与标记运行阶段都在同一条链路内。
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
		Input:              content,
	}
	ctx = m.startTrace(ctx, request)
	if !m.dispatch(ctx, request) {
		m.finishTrace(ctx, errManagerClosing)
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

// Cancel 停止一个活跃的 run。它是幂等的，对已处于终态的 run 返回 nil。
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

// RecoverInterrupted 修复上一个进程遗留的活跃 run。
func (m *Manager) RecoverInterrupted(ctx context.Context) error {
	return m.store.RecoverInterrupted(ctx)
}

// Shutdown 取消所有 worker 并等待其结束，等待时长以 ctx 为上限。关闭期间
// 被取消的 run 会被记录为 interrupted。
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
	markCtx, endMark := m.startSpan(ctx, spanMarkRunning)
	started, err := m.store.MarkRunning(context.WithoutCancel(markCtx), request)
	endMark(err)
	if err != nil {
		slog.Error("mark run running", logKeyRunID, request.RunID,
			logKeyError, logging.Redact(err.Error()))
		m.failRun(dbCtx, request.RunID, logging.Redact(err.Error()))
		m.finishTrace(ctx, err)
		return
	}
	if !started {
		m.finishTrace(ctx, nil)
		return
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		m.finish(dbCtx, request, ctxErr)
		m.finishTrace(ctx, ctxErr)
		return
	}
	runErr := m.runner.Run(ctx, request)
	if runErr == nil {
		// 标题在 run.completed 事件之前落库，前端收到终态刷新会话列表时
		// 就能拿到可读标题，不必再额外轮询。
		m.nameSession(ctx, request.SessionID)
	}
	m.finish(dbCtx, request, runErr)
	m.finishTrace(ctx, runErr)
}

// nameSession 为新会话生成可读标题；失败只记录日志，不影响 run 终态。
func (m *Manager) nameSession(ctx context.Context, sessionID uuid.UUID) {
	if m.titles == nil {
		return
	}
	if err := m.titles.NameSession(ctx, sessionID); err != nil {
		slog.Warn("name session", logKeySessionID, sessionID,
			logKeyError, logging.Redact(err.Error()))
	}
}

// handlePanic 将 worker 的 panic 记录为失败的 run；panic 值脱敏后写入
// error_message，并随 run.failed 事件发布。
func (m *Manager) handlePanic(ctx context.Context, request Request, recovered any) {
	detail := logging.Redact(fmt.Sprintf("%v", recovered))
	slog.Error("run worker panic", logKeyRunID, request.RunID, logKeyError, detail)
	m.failRun(context.WithoutCancel(ctx), request.RunID, detail)
	m.finishTrace(ctx, errors.New(detail))
}

// startTrace 在 run 层建立根观测；未配置追踪时原样返回 ctx。
func (m *Manager) startTrace(ctx context.Context, request Request) context.Context {
	if m.tracer == nil {
		return ctx
	}
	return m.tracer.StartRun(ctx, tracing.TraceMeta{
		RunID: request.RunID, SessionID: request.SessionID, Input: request.Input,
	})
}

// startSpan 在 run 层创建子观测；未配置追踪时返回空收尾函数。
func (m *Manager) startSpan(
	ctx context.Context, name string,
) (context.Context, func(error)) {
	if m.tracer == nil {
		return ctx, func(error) {}
	}
	return m.tracer.StartSpan(ctx, name)
}

// finishTrace 结束 run 层建立的根观测。
func (m *Manager) finishTrace(ctx context.Context, err error) {
	if m.tracer == nil {
		return
	}
	if err == nil {
		m.tracer.FinishRun(ctx, emptyText)
		return
	}
	m.tracer.FinishRun(ctx, err.Error())
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
	case errors.As(runErr, &coded):
		// 模型请求超时会包装 DeadlineExceeded，必须优先按错误码归因
		slog.Error("run failed", logKeyRunID, request.RunID,
			logKeyError, logging.Redact(runErr.Error()))
		m.finishRun(ctx, FinishInput{
			RunID:         request.RunID,
			Status:        StatusFailed,
			MessageStatus: messageFailed,
			ErrorCode:     coded.ErrorCode(),
			ErrorMessage:  logging.Redact(runErr.Error()),
			EventType:     eventRunFailed,
		})
	case errors.Is(runErr, context.DeadlineExceeded):
		m.finishRun(ctx, FinishInput{
			RunID:         request.RunID,
			Status:        StatusFailed,
			MessageStatus: messageFailed,
			ErrorCode:     codeRunTimeout,
			ErrorMessage:  "run timed out",
			EventType:     eventRunFailed,
		})
	default:
		slog.Error("run failed", logKeyRunID, request.RunID,
			logKeyError, logging.Redact(runErr.Error()))
		m.failRun(ctx, request.RunID, logging.Redact(runErr.Error()))
	}
}

func (m *Manager) failRun(ctx context.Context, runID uuid.UUID, message string) {
	m.finishRun(ctx, FinishInput{
		RunID:         runID,
		Status:        StatusFailed,
		MessageStatus: messageFailed,
		ErrorCode:     codeRunFailed,
		ErrorMessage:  message,
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

// newRunContext 将 run 与 HTTP 请求的生命周期解耦。WithoutCancel 先剥离
// 请求的取消信号和截止时间，再由 MaxDuration 设置 run 唯一遵循的截止时间。
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
