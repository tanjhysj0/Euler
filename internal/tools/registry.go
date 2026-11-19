package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"euler/internal/approval"
	"euler/internal/event"
	"euler/internal/logging"
	"euler/internal/persistence"
	"euler/internal/workspace"
)

// ErrToolCallLimit aborts a run whose tool call budget is exhausted.
var ErrToolCallLimit = errors.New("tool call limit exceeded")

// CodedError carries a stable run error code alongside a redacted message and
// the original cause. It satisfies the run manager's coded error contract
// without importing the run package.
type CodedError struct {
	Code string
	Msg  string
	Err  error
}

// Error returns the redacted, user safe message.
func (e *CodedError) Error() string { return e.Msg }

// Unwrap exposes the original cause for errors.Is and errors.As.
func (e *CodedError) Unwrap() error { return e.Err }

// ErrorCode returns the stable run error code.
func (e *CodedError) ErrorCode() string { return e.Code }

const (
	toolStatusPending   = "pending"
	toolStatusSucceeded = "succeeded"
	toolStatusFailed    = "failed"

	eventToolStarted   = "tool.started"
	eventToolCompleted = "tool.completed"
	eventToolFailed    = "tool.failed"

	codeToolCallLimit = "tool_call_limit"
	rejectedMessage   = "用户拒绝"

	singleToolCall   = 1
	noOutputLimit    = 0
	emptyObservation = ""

	singleItem           = 1
	observationCompleted = "completed"
)

// RegistryDeps carries the per-run dependencies every tool invocation needs.
type RegistryDeps struct {
	DB             *gorm.DB
	Events         event.Publisher
	Gate           approval.Gate
	RunID          uuid.UUID
	MaxToolCalls   int
	MaxOutputBytes int
	APIKey         string
}

// Registry adapts host tools to Eino: every invocation persists a ToolCall
// row, emits lifecycle events, enforces the run's call budget and routes
// sensitive side effects through the approval gate.
type Registry struct {
	deps  RegistryDeps
	calls atomic.Int64
}

// NewRegistry builds a registry for one run.
func NewRegistry(deps RegistryDeps) *Registry { return &Registry{deps: deps} }

// Tools assembles the seven agent tools bound to one workspace directory.
func (r *Registry) Tools(
	files *workspace.FileStore, shell ShellConfig, dir string, secrets []string,
) ([]tool.BaseTool, error) {
	built, err := r.fileTools(files)
	if err != nil {
		return nil, err
	}
	shellTool, err := r.shellTool(NewShellExecutor(shell), dir, secrets)
	if err != nil {
		return nil, err
	}
	return append(built, shellTool), nil
}

// toolOutcome is what a tool function reports back to the registry. Read-only
// tools fill observation directly; sensitive tools fill payload plus apply,
// which only runs after the approval gate grants the call.
type toolOutcome struct {
	payload     map[string]any
	apply       func(context.Context) (toolOutcome, error)
	observation string
	failed      bool
}

type toolInvocation struct {
	name string
	args any
	fn   func(context.Context) (toolOutcome, error)
}

func registerTool[T any](
	r *Registry, name, desc string, fn func(context.Context, T) (toolOutcome, error),
) (tool.BaseTool, error) {
	return utils.InferTool(name, desc, func(ctx context.Context, input T) (string, error) {
		return r.invoke(ctx, toolInvocation{
			name: name,
			args: input,
			fn:   func(ctx context.Context) (toolOutcome, error) { return fn(ctx, input) },
		})
	})
}

func (r *Registry) invoke(ctx context.Context, call toolInvocation) (string, error) {
	row, err := r.openCall(ctx, call)
	if err != nil {
		return emptyObservation, err
	}
	if r.calls.Add(singleToolCall) > int64(r.deps.MaxToolCalls) {
		return emptyObservation, r.budgetExceeded(ctx, row.ID)
	}
	if err = r.appendEvent(ctx, eventToolStarted, map[string]any{
		"tool_call_id":  row.ID.String(),
		"name":          call.name,
		"arguments":     json.RawMessage(row.ArgumentsJSON),
		"model_call_id": compose.GetToolCallID(ctx),
	}); err != nil {
		return emptyObservation, err
	}
	outcome, err := call.fn(ctx)
	if err != nil {
		r.failCall(ctx, row.ID, err)
		return emptyObservation, err
	}
	if outcome.apply == nil {
		return r.finalize(ctx, row.ID, call.name, outcome)
	}
	return r.guarded(ctx, row.ID, call.name, outcome)
}

func (r *Registry) guarded(
	ctx context.Context, callID uuid.UUID, name string, outcome toolOutcome,
) (string, error) {
	decision, err := r.deps.Gate.Require(ctx, approval.Request{
		RunID: r.deps.RunID, ToolCallID: callID, Name: name, Payload: outcome.payload,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return emptyObservation, ctxErr
		}
		return emptyObservation, err
	}
	if decision.Status == approval.StatusCancelled {
		return emptyObservation, context.Canceled
	}
	if !decision.Approved {
		return r.recordRejected(ctx, callID, name)
	}
	applied, err := outcome.apply(ctx)
	if err != nil {
		return emptyObservation, err
	}
	return r.finalize(ctx, callID, name, applied)
}

// recordRejected stores only the observation; the approval service already
// moved the ToolCall row to rejected with a finished timestamp.
func (r *Registry) recordRejected(
	ctx context.Context, callID uuid.UUID, name string,
) (string, error) {
	body := observationBody("rejected", map[string]any{"message": rejectedMessage})
	err := r.deps.DB.WithContext(ctx).Model(&persistence.ToolCall{}).
		Where("id = ?", callID).Update("result_json", datatypes.JSON(body)).Error
	if err != nil {
		return emptyObservation, fmt.Errorf("record rejected tool call: %w", err)
	}
	if err = r.appendEvent(ctx, eventToolCompleted, resultPayload(callID, name, body)); err != nil {
		return emptyObservation, err
	}
	return body, nil
}

func (r *Registry) finalize(
	ctx context.Context, callID uuid.UUID, name string, outcome toolOutcome,
) (string, error) {
	status, eventType := toolStatusSucceeded, eventToolCompleted
	if outcome.failed {
		status, eventType = toolStatusFailed, eventToolFailed
	}
	body := logging.Redact(outcome.observation, r.deps.APIKey)
	now := time.Now().UTC()
	err := r.deps.DB.WithContext(ctx).Model(&persistence.ToolCall{}).
		Where("id = ?", callID).Updates(map[string]any{
		"status": status, "result_json": datatypes.JSON(body), "finished_at": now,
	}).Error
	if err != nil {
		return emptyObservation, fmt.Errorf("finish tool call: %w", err)
	}
	if err = r.appendEvent(ctx, eventType, resultPayload(callID, name, body)); err != nil {
		return emptyObservation, err
	}
	return body, nil
}

func (r *Registry) openCall(
	ctx context.Context, call toolInvocation,
) (persistence.ToolCall, error) {
	now := time.Now().UTC()
	row := persistence.ToolCall{
		ID: uuid.New(), RunID: r.deps.RunID, Name: call.name,
		ArgumentsJSON: datatypes.JSON(r.encodeArgs(call.args)),
		Status:        toolStatusPending, CreatedAt: now, StartedAt: &now,
	}
	if err := r.deps.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return persistence.ToolCall{}, fmt.Errorf("create tool call: %w", err)
	}
	return row, nil
}

func (r *Registry) encodeArgs(args any) string {
	data, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return logging.Redact(string(jsonbSafe(data)), r.deps.APIKey)
}

func (r *Registry) budgetExceeded(ctx context.Context, callID uuid.UUID) error {
	err := &CodedError{
		Code: codeToolCallLimit, Msg: ErrToolCallLimit.Error(), Err: ErrToolCallLimit,
	}
	r.failCall(ctx, callID, err)
	return err
}

func (r *Registry) failCall(ctx context.Context, callID uuid.UUID, cause error) {
	now := time.Now().UTC()
	message := logging.Redact(cause.Error(), r.deps.APIKey)
	err := r.deps.DB.WithContext(ctx).Model(&persistence.ToolCall{}).
		Where("id = ?", callID).Updates(map[string]any{
		"status": toolStatusFailed, "error_message": message, "finished_at": now,
	}).Error
	if err != nil {
		slog.Error("mark tool call failed", "tool_call_id", callID, "error", err)
	}
}

func (r *Registry) appendEvent(ctx context.Context, eventType string, payload any) error {
	if _, err := r.deps.Events.Append(ctx, r.deps.RunID, eventType, payload); err != nil {
		return fmt.Errorf("append %s event: %w", eventType, err)
	}
	return nil
}

// truncate caps raw payloads before they are embedded into observation JSON,
// keeping the stored jsonb document valid while bounding the model's input.
func (r *Registry) truncate(text string) string {
	if r.deps.MaxOutputBytes > noOutputLimit && len(text) > r.deps.MaxOutputBytes {
		return text[:r.deps.MaxOutputBytes]
	}
	return text
}

// cappedList shrinks a list payload from the tail until its marshaled
// observation fits MaxOutputBytes, flagging truncation. It never byte-cuts the
// final JSON (Ruling 3), so the stored jsonb document stays valid.
func cappedList[T any](r *Registry, key string, items []T) map[string]any {
	payload := map[string]any{key: items}
	if r.deps.MaxOutputBytes <= noOutputLimit ||
		len(observationBody(observationCompleted, payload)) <= r.deps.MaxOutputBytes {
		return payload
	}
	payload["truncated"] = true
	for len(items) > emptyCapacity &&
		len(observationBody(observationCompleted, payload)) > r.deps.MaxOutputBytes {
		items = items[:len(items)-singleItem]
		payload[key] = items
	}
	return payload
}

// jsonbSafe rewrites the escaped NUL sequence to the escaped replacement
// character: PostgreSQL jsonb rejects NUL escapes in stored documents.
func jsonbSafe(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("\\u0000"), []byte("\\ufffd"))
}

func resultPayload(callID uuid.UUID, name, body string) map[string]any {
	return map[string]any{
		"tool_call_id": callID.String(),
		"name":         name,
		"result":       json.RawMessage(body),
	}
}

func completedOutcome(extra map[string]any) toolOutcome {
	return toolOutcome{observation: observationBody(observationCompleted, extra)}
}

func failedOutcome(extra map[string]any) toolOutcome {
	return toolOutcome{observation: observationBody(toolStatusFailed, extra), failed: true}
}

func observationBody(status string, extra map[string]any) string {
	body := map[string]any{"status": status}
	for key, value := range extra {
		body[key] = value
	}
	data, err := json.Marshal(body)
	if err != nil {
		return `{"status":"failed"}`
	}
	return string(jsonbSafe(data))
}
