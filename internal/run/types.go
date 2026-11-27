// Package run 负责 run 的状态迁移、按会话的并发控制、取消以及启动恢复。
package run

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"euler/internal/persistence"
)

// Run 状态。每个会话最多只能有一个 run 处于活跃状态。
const (
	StatusQueued          = "queued"
	StatusRunning         = "running"
	StatusWaitingApproval = "waiting_approval"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
	StatusInterrupted     = "interrupted"
)

const (
	messageStreaming   = "streaming"
	messageCompleted   = "completed"
	messageFailed      = "failed"
	messageCancelled   = "cancelled"
	messageInterrupted = "interrupted"

	eventRunStarted     = "run.started"
	eventMessageStarted = "message.started"
	eventRunCompleted   = "run.completed"
	eventRunFailed      = "run.failed"
	eventRunCancelled   = "run.cancelled"
	eventRunInterrupted = "run.interrupted"
	codeRunTimeout      = "run_timeout"
	codeRunFailed       = "run_failed"
	emptyText           = ""
	sessionSeqStep      = 1
	initialNextEventSeq = 1
	emptyLastEventSeq   = 0
	noCapacity          = 0
	noSequence          = 0
	zeroCount           = 0
	singleWorker        = 1
	logKeyRunID         = "run_id"
	logKeySessionID     = "session_id"
	logKeyError         = "error"
)

var (
	// ErrActiveRun 表示该会话已存在一个活跃的 run。
	ErrActiveRun = errors.New("active run exists")
	// ErrNotFound 表示 run 或会话不存在。
	ErrNotFound = errors.New("run not found")
	// ErrInvalidInput 表示无法接受的 run 输入。
	ErrInvalidInput = errors.New("invalid run input")

	// errActiveRunConflict 标记 runs 插入时触发的唯一键冲突。
	// runs 的主键是随机 UUID，因此冲突只可能来自
	// runs_one_active_idx 部分唯一索引。
	errActiveRunConflict = errors.New("active run conflict")

	activeStatuses = []string{StatusQueued, StatusRunning, StatusWaitingApproval}
)

// allowedTransitions 列出状态机的全部迁移边。interrupted 目标状态仅保留给
// 恢复修复路径，不会作为值出现在此表中。
var allowedTransitions = map[string]map[string]bool{
	StatusQueued: {
		StatusRunning: true, StatusCancelled: true, StatusFailed: true,
	},
	StatusRunning: {
		StatusWaitingApproval: true, StatusSucceeded: true,
		StatusFailed: true, StatusCancelled: true,
	},
	StatusWaitingApproval: {
		StatusRunning: true, StatusCancelled: true, StatusFailed: true,
	},
}

// CanTransition 报告状态机是否允许 from -> to 的迁移。interrupted 目标状态
// 仅保留给恢复修复路径，因此这里永远不会接受它。
func CanTransition(from, to string) bool {
	return allowedTransitions[from][to]
}

// Request 是传递给 Runner 的不可变输入。
type Request struct {
	RunID              uuid.UUID
	SessionID          uuid.UUID
	WorkspaceID        uuid.UUID
	AssistantMessageID uuid.UUID
	// Input 是触发本次 run 的用户消息，供 run 层建立 trace 时记录。
	Input string
}

// Runner 执行一个 run，直到完成、失败或被取消。
type Runner interface {
	Run(ctx context.Context, request Request) error
}

// RunStore 是 Manager 依赖的持久化端口。Store 是生产实现，
// 测试可以注入内存实现来验证编排行为。
type RunStore interface {
	Start(ctx context.Context, sessionID uuid.UUID, content string) (StartResult, error)
	MarkRunning(ctx context.Context, request Request) (bool, error)
	Finish(ctx context.Context, input FinishInput) error
	Cancel(ctx context.Context, runID uuid.UUID) (Snapshot, error)
	Interrupt(ctx context.Context, runID uuid.UUID) error
	RecoverInterrupted(ctx context.Context) error
}

// RunnerFunc 将一个函数适配为 Runner 接口。
type RunnerFunc func(ctx context.Context, request Request) error

// Run 使用传入的 context 和 request 调用 f。
func (f RunnerFunc) Run(ctx context.Context, request Request) error {
	return f(ctx, request)
}

// Snapshot 是用于 API 响应的 run 权威状态。
type Snapshot struct {
	Run          persistence.Run        `json:"run"`
	ToolCalls    []persistence.ToolCall `json:"tool_calls"`
	Approvals    []persistence.Approval `json:"approvals"`
	LastEventSeq int64                  `json:"last_event_seq"`
}

// StartResult 是新启动的 run 及其已持久化的消息。
type StartResult struct {
	Snapshot
	UserMessage      persistence.Message `json:"user_message"`
	AssistantMessage persistence.Message `json:"assistant_message"`
	WorkspaceWarning bool                `json:"workspace_warning"`
	WorkspaceID      uuid.UUID           `json:"-"`
}

func isTerminal(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusInterrupted:
		return true
	}
	return false
}
