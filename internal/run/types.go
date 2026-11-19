// Package run owns run state transitions, per-session concurrency, cancellation
// and startup recovery.
package run

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"euler/internal/persistence"
)

// Run statuses. Active statuses may be held by at most one run per session.
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
	sessionSeqStep      = 1
	initialNextEventSeq = 1
	emptyLastEventSeq   = 0
	noCapacity          = 0
	noSequence          = 0
	zeroCount           = 0
	singleWorker        = 1
	logKeyRunID         = "run_id"
	logKeyError         = "error"
)

var (
	// ErrActiveRun reports that the session already has an active run.
	ErrActiveRun = errors.New("active run exists")
	// ErrNotFound reports that a run or session does not exist.
	ErrNotFound = errors.New("run not found")
	// ErrInvalidInput reports run input that cannot be accepted.
	ErrInvalidInput = errors.New("invalid run input")

	// errActiveRunConflict marks a duplicate key raised by the runs insert.
	// The runs primary key is a random UUID, so a duplicate can only come from
	// the runs_one_active_idx partial unique index.
	errActiveRunConflict = errors.New("active run conflict")

	activeStatuses = []string{StatusQueued, StatusRunning, StatusWaitingApproval}
)

// allowedTransitions lists every state machine edge. The interrupted target is
// reserved for the recovery repair path and never appears as a value.
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

// CanTransition reports whether the state machine allows from -> to. The
// interrupted target is reserved for the recovery repair path, so it is never
// accepted here.
func CanTransition(from, to string) bool {
	return allowedTransitions[from][to]
}

// Request is the immutable input handed to a Runner.
type Request struct {
	RunID              uuid.UUID
	SessionID          uuid.UUID
	WorkspaceID        uuid.UUID
	AssistantMessageID uuid.UUID
}

// Runner executes one run until completion, failure or cancellation.
type Runner interface {
	Run(ctx context.Context, request Request) error
}

// RunnerFunc adapts a function to the Runner interface.
type RunnerFunc func(ctx context.Context, request Request) error

// Run calls f with the supplied context and request.
func (f RunnerFunc) Run(ctx context.Context, request Request) error {
	return f(ctx, request)
}

// Snapshot is the authoritative state of a run for API responses.
type Snapshot struct {
	Run          persistence.Run        `json:"run"`
	ToolCalls    []persistence.ToolCall `json:"tool_calls"`
	Approvals    []persistence.Approval `json:"approvals"`
	LastEventSeq int64                  `json:"last_event_seq"`
}

// StartResult is a newly started run together with its persisted messages.
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
