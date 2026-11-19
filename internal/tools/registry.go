package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"

	"euler/internal/approval"
	"euler/internal/event"
	"euler/internal/logging"
)

// ErrToolCallLimit 用于中止工具调用预算耗尽的运行。
var ErrToolCallLimit = errors.New("tool call limit exceeded")

// CodedError 携带稳定的运行错误码，以及脱敏后的消息和原始原因。
// 它在不导入 run 包的情况下满足 run 管理器的带码错误约定。
type CodedError struct {
	Code string
	Msg  string
	Err  error
}

// Error 返回脱敏后、可安全展示给用户的消息。
func (e *CodedError) Error() string { return e.Msg }

// Unwrap 暴露原始原因，供 errors.Is 和 errors.As 使用。
func (e *CodedError) Unwrap() error { return e.Err }

// ErrorCode 返回稳定的运行错误码。
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

// RegistryDeps 携带每次工具调用所需的、按运行划分的依赖。
type RegistryDeps struct {
	Calls          CallRecorder
	Events         event.Publisher
	Gate           approval.Gate
	RunID          uuid.UUID
	MaxToolCalls   int
	MaxOutputBytes int
	APIKey         string
}

// Registry 将宿主工具适配到 Eino：每次调用都会持久化一条 ToolCall 记录、
// 发出生命周期事件、执行本次运行的调用预算限制，
// 并把敏感副作用路由到审批关卡。
type Registry struct {
	deps  RegistryDeps
	calls atomic.Int64
}

// NewRegistry 为一次运行构建注册表。
func NewRegistry(deps RegistryDeps) *Registry { return &Registry{deps: deps} }

// Tools 组装绑定到单个工作区目录的七个 agent 工具。
func (r *Registry) Tools(
	files FileStore, executor Executor, dir string, secrets []string,
) ([]tool.BaseTool, error) {
	built, err := r.fileTools(files)
	if err != nil {
		return nil, err
	}
	shellTool, err := r.shellTool(executor, dir, secrets)
	if err != nil {
		return nil, err
	}
	return append(built, shellTool), nil
}

// toolOutcome 是工具函数向注册表回报的结果。只读工具直接填写 observation；
// 敏感工具填写 payload 和 apply，apply 只有在审批关卡放行该调用后才会执行。
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
	rowID := uuid.New()
	argsJSON := r.encodeArgs(call.args)
	if err := r.deps.Calls.Start(ctx, Call{
		ID:            rowID,
		RunID:         r.deps.RunID,
		Name:          call.name,
		ArgumentsJSON: []byte(argsJSON),
	}); err != nil {
		return emptyObservation, err
	}
	if r.calls.Add(singleToolCall) > int64(r.deps.MaxToolCalls) {
		return emptyObservation, r.budgetExceeded(ctx, rowID)
	}
	if err := r.appendEvent(ctx, eventToolStarted, map[string]any{
		"tool_call_id":  rowID.String(),
		"name":          call.name,
		"arguments":     json.RawMessage(argsJSON),
		"model_call_id": compose.GetToolCallID(ctx),
	}); err != nil {
		return emptyObservation, err
	}
	outcome, err := call.fn(ctx)
	if err != nil {
		r.failCall(ctx, rowID, err)
		return emptyObservation, err
	}
	if outcome.apply == nil {
		return r.finalize(ctx, rowID, call.name, outcome)
	}
	return r.guarded(ctx, rowID, call.name, outcome)
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

// recordRejected 只存储 observation；审批服务已将 ToolCall 记录
// 置为 rejected 并写入完成时间戳。
func (r *Registry) recordRejected(
	ctx context.Context, callID uuid.UUID, name string,
) (string, error) {
	body := observationBody("rejected", map[string]any{"message": rejectedMessage})
	if err := r.deps.Calls.Reject(ctx, callID, body); err != nil {
		return emptyObservation, err
	}
	if err := r.appendEvent(
		ctx, eventToolCompleted, resultPayload(callID, name, body),
	); err != nil {
		return emptyObservation, err
	}
	return body, nil
}

func (r *Registry) finalize(
	ctx context.Context, callID uuid.UUID, name string, outcome toolOutcome,
) (string, error) {
	eventType := eventToolCompleted
	if outcome.failed {
		eventType = eventToolFailed
	}
	body := logging.Redact(outcome.observation, r.deps.APIKey)
	if outcome.failed {
		if err := r.deps.Calls.Fail(ctx, callID, body, emptyObservation); err != nil {
			return emptyObservation, err
		}
	} else if err := r.deps.Calls.Succeed(ctx, callID, body); err != nil {
		return emptyObservation, err
	}
	if err := r.appendEvent(ctx, eventType, resultPayload(callID, name, body)); err != nil {
		return emptyObservation, err
	}
	return body, nil
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
	message := logging.Redact(cause.Error(), r.deps.APIKey)
	if err := r.deps.Calls.Fail(ctx, callID, "", message); err != nil {
		slog.Error("mark tool call failed", "tool_call_id", callID, "error", err)
	}
}

func (r *Registry) appendEvent(ctx context.Context, eventType string, payload any) error {
	if _, err := r.deps.Events.Append(ctx, r.deps.RunID, eventType, payload); err != nil {
		return fmt.Errorf("append %s event: %w", eventType, err)
	}
	return nil
}

// truncate 在原始负载嵌入 observation JSON 之前对其限长，
// 在限制模型输入的同时保证存储的 jsonb 文档有效。
func (r *Registry) truncate(text string) string {
	if r.deps.MaxOutputBytes > noOutputLimit && len(text) > r.deps.MaxOutputBytes {
		return text[:r.deps.MaxOutputBytes]
	}
	return text
}

// cappedList 从尾部收缩列表负载，直到其序列化后的 observation
// 不超过 MaxOutputBytes，并标记截断。它绝不对最终 JSON 做字节级截断
// （Ruling 3），因此存储的 jsonb 文档始终有效。
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

// jsonbSafe 将转义的 NUL 序列改写为转义的替换字符：
// PostgreSQL jsonb 拒绝存储文档中的 NUL 转义。
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
