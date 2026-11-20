package tracing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Langfuse OTel 属性名；显式设置这些属性可避免依赖通用约定推断。
const (
	attrTraceName        = "langfuse.trace.name"
	attrTraceTags        = "langfuse.trace.tags"
	attrTraceMetaPrefix  = "langfuse.trace.metadata."
	attrSessionID        = "langfuse.session.id"
	attrEnvironment      = "langfuse.environment"
	attrObsType          = "langfuse.observation.type"
	attrObsInput         = "langfuse.observation.input"
	attrObsOutput        = "langfuse.observation.output"
	attrObsLevel         = "langfuse.observation.level"
	attrObsStatus        = "langfuse.observation.status_message"
	attrObsModel         = "langfuse.observation.model.name"
	attrObsModelParams   = "langfuse.observation.model.parameters"
	attrObsUsage         = "langfuse.observation.usage_details"
	attrObsCompletion    = "langfuse.observation.completion_start_time"
	attrObsMetaPrefix    = "langfuse.observation.metadata."
	obsTypeAgent         = "agent"
	obsTypeGeneration    = "generation"
	obsTypeTool          = "tool"
	obsTypeSpan          = "span"
	levelError           = "ERROR"
	spanGenerateResponse = "generate-response"
	observationAttrCap   = 8
)

// handler 把 Eino 的模型与工具回调映射为 Langfuse 观测，
// 其余组件（图、节点包装器）不生成 span，避免噪音。
type handler struct {
	tracer trace.Tracer
}

func newHandler(tracer trace.Tracer) *handler {
	return &handler{tracer: tracer}
}

// OnStart 在模型或工具调用开始时创建观测。
func (h *handler) OnStart(
	ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput,
) context.Context {
	if info == nil {
		return ctx
	}
	switch info.Component { //nolint:exhaustive // 只处理模型与工具组件
	case components.ComponentOfChatModel:
		return h.startGeneration(ctx, input)
	case components.ComponentOfTool:
		return h.startTool(ctx, info, input)
	default:
		return ctx
	}
}

// OnEnd 在非流式调用结束时补全输出并结束观测。
func (h *handler) OnEnd(
	ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput,
) context.Context {
	if info == nil {
		return ctx
	}
	state := spanStateFrom(ctx)
	if state == nil {
		return ctx
	}
	switch info.Component { //nolint:exhaustive // 只处理模型与工具组件
	case components.ComponentOfChatModel:
		h.finishGeneration(ctx, state, output)
	case components.ComponentOfTool:
		h.finishTool(ctx, state, output)
	default:
		return ctx
	}
	state.span.End()
	return ctx
}

// OnError 把失败写入观测并标记 ERROR，便于在 Langfuse 中直接定位失败步骤。
func (*handler) OnError(
	ctx context.Context, info *callbacks.RunInfo, err error,
) context.Context {
	if info == nil || err == nil {
		return ctx
	}
	state := spanStateFrom(ctx)
	if state == nil {
		return ctx
	}
	markSpanError(ctx, state.span, err)
	state.span.End()
	return ctx
}

// markSpanError 把失败原因脱敏后写入观测并标记 ERROR，
// 供同步回调与流式消费两条路径共用。
func markSpanError(ctx context.Context, span trace.Span, err error) {
	reason := redact(ctx, err.Error())
	span.SetAttributes(
		attribute.String(attrObsLevel, levelError),
		attribute.String(attrObsStatus, reason),
	)
	span.RecordError(errors.New(reason))
	span.SetStatus(codes.Error, reason)
}

// OnStartWithStreamInput 只需关闭副本；当前模型与工具均使用普通输入。
func (*handler) OnStartWithStreamInput(
	ctx context.Context, _ *callbacks.RunInfo,
	input *schema.StreamReader[callbacks.CallbackInput],
) context.Context {
	input.Close()
	return ctx
}

// OnEndWithStreamOutput 在后台消费输出流副本，补全 generation/tool 观测。
func (h *handler) OnEndWithStreamOutput(
	ctx context.Context, info *callbacks.RunInfo,
	output *schema.StreamReader[callbacks.CallbackOutput],
) context.Context {
	if info == nil {
		output.Close()
		return ctx
	}
	state := spanStateFrom(ctx)
	if state == nil {
		output.Close()
		return ctx
	}
	switch info.Component { //nolint:exhaustive // 只处理模型与工具组件
	case components.ComponentOfChatModel:
		go h.finishGenerationStream(ctx, state, output)
	case components.ComponentOfTool:
		go h.finishToolStream(ctx, state, output)
	default:
		output.Close()
	}
	return ctx
}

// startGeneration 创建 generation 观测，记录模型、参数与完整输入消息。
func (h *handler) startGeneration(
	ctx context.Context, input callbacks.CallbackInput,
) context.Context {
	msgInput := model.ConvCallbackInput(input)
	if msgInput == nil {
		return ctx
	}
	attrs := observationAttrs(ctx, obsTypeGeneration)
	attrs = append(attrs, modelAttrs(ctx, msgInput)...)
	if len(msgInput.Messages) > noItems {
		attrs = append(attrs, attribute.String(
			attrObsInput, redact(ctx, messagesJSON(msgInput.Messages)),
		))
	}
	ctx, span := h.tracer.Start(ctx, spanGenerateResponse, trace.WithAttributes(attrs...))
	if rc := runContextFrom(ctx); rc != nil {
		rc.rememberGeneration(span.SpanContext())
	}
	return withSpanState(ctx, &spanState{span: span, started: time.Now()})
}

// modelAttrs 返回模型名与调用参数；模型名缺失时回退到 run 级配置。
func modelAttrs(
	ctx context.Context, input *model.CallbackInput,
) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	name := emptyValue
	if input.Config != nil {
		name = input.Config.Model
		if params := modelParamsJSON(input.Config); params != emptyValue {
			attrs = append(attrs, attribute.String(attrObsModelParams, params))
		}
	}
	if name == emptyValue {
		if rc := runContextFrom(ctx); rc != nil {
			name = rc.model
		}
	}
	if name == emptyValue {
		return attrs
	}
	return append(attrs, attribute.String(attrObsModel, name))
}

// startTool 创建 tool 观测；span 名使用工具名，保持低基数且稳定。
func (h *handler) startTool(
	ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput,
) context.Context {
	name := info.Name
	if name == emptyValue {
		name = obsTypeTool
	}
	attrs := observationAttrs(ctx, obsTypeTool)
	attrs = append(attrs, attribute.String(attrObsMetaPrefix+"tool_name", name))
	callID := compose.GetToolCallID(ctx)
	if callID != emptyValue {
		attrs = append(attrs, attribute.String(attrObsMetaPrefix+"tool_call_id", callID))
	}
	// 工具观测挂到发起它的 generation 之下，便于在 Langfuse 中查看调用归属
	if rc := runContextFrom(ctx); rc != nil {
		if parent, ok := rc.generationParent(callID); ok {
			ctx = trace.ContextWithSpanContext(ctx, parent)
		}
	}
	if toolInput := tool.ConvCallbackInput(input); toolInput != nil &&
		toolInput.ArgumentsInJSON != emptyValue {
		attrs = append(attrs, attribute.String(
			attrObsInput, redact(ctx, toolInput.ArgumentsInJSON),
		))
	}
	ctx, span := h.tracer.Start(ctx, name, trace.WithAttributes(attrs...))
	return withSpanState(ctx, &spanState{span: span, started: time.Now()})
}

// finishGeneration 为同步模型调用补全输出、用量与推理内容。
func (*handler) finishGeneration(
	ctx context.Context, state *spanState, output callbacks.CallbackOutput,
) {
	out := model.ConvCallbackOutput(output)
	if out == nil {
		return
	}
	attrs := generationOutputAttrs(ctx, out.Message)
	attrs = append(attrs, usageAttr(out.TokenUsage, out.Message)...)
	attrs = append(attrs, completionStartAttr(state))
	state.span.SetAttributes(attrs...)
	rememberToolParents(ctx, out.Message, state.span)
}

// finishTool 为同步工具调用写入 observation 输出。
func (*handler) finishTool(
	ctx context.Context, state *spanState, output callbacks.CallbackOutput,
) {
	out := tool.ConvCallbackOutput(output)
	if out == nil {
		return
	}
	state.span.SetAttributes(attribute.String(
		attrObsOutput, redact(ctx, jsonOrText(toolOutputText(out))),
	))
}

// finishGenerationStream 消费流式输出副本并结束 generation 观测。
func (*handler) finishGenerationStream(
	ctx context.Context, state *spanState, stream *schema.StreamReader[callbacks.CallbackOutput],
) {
	defer func() {
		stream.Close()
		if recovered := recover(); recovered != nil {
			slog.Error("trace generation panic", "error", recovered,
				"stack", string(debug.Stack()))
			state.span.End()
		}
	}()
	drained := drainModelStream(stream)
	var merged *schema.Message
	if len(drained.messages) > noItems {
		result, err := schema.ConcatMessages(drained.messages)
		if err == nil {
			merged = result
		} else if drained.err == nil {
			drained.err = err
		}
	}
	attrs := generationOutputAttrs(ctx, merged)
	attrs = append(attrs, usageAttr(drained.usage, merged)...)
	// 流式的完成开始时间取首个有效分片，才能反映真实的 TTFT
	attrs = append(attrs, completionStartAt(state, drained.firstToken))
	state.span.SetAttributes(attrs...)
	rememberToolParents(ctx, merged, state.span)
	// 流内错误不会触发 OnError，必须在这里主动标记，否则会显示为成功
	if drained.err != nil {
		markSpanError(ctx, state.span, drained.err)
	}
	state.span.End()
}

// finishToolStream 消费流式工具输出副本并结束 tool 观测。
func (*handler) finishToolStream(
	ctx context.Context, state *spanState, stream *schema.StreamReader[callbacks.CallbackOutput],
) {
	defer func() {
		stream.Close()
		if recovered := recover(); recovered != nil {
			slog.Error("trace tool panic", "error", recovered, "stack", string(debug.Stack()))
			state.span.End()
		}
	}()
	var outputs []string
	var streamErr error
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			streamErr = err
			break
		}
		if out := tool.ConvCallbackOutput(chunk); out != nil {
			outputs = append(outputs, toolOutputText(out))
		}
	}
	state.span.SetAttributes(attribute.String(
		attrObsOutput, redact(ctx, jsonOrText(joinStrings(outputs))),
	))
	// 流内错误不会触发 OnError，必须在这里主动标记，否则会显示为成功
	if streamErr != nil {
		markSpanError(ctx, state.span, streamErr)
	}
	state.span.End()
}

// drainedStream 汇总一次模型流消费的结果。
type drainedStream struct {
	messages   []*schema.Message
	usage      *model.TokenUsage
	firstToken time.Time
	err        error
}

// drainModelStream 读取模型输出流，返回待合并的消息、最后一次记录的用量、
// 首个有效分片时间以及流内错误；错误必须由调用方标记，
// 因为 Eino 不会为流内失败触发 OnError。
func drainModelStream(
	stream *schema.StreamReader[callbacks.CallbackOutput],
) drainedStream {
	var result drainedStream
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			result.err = err
			return result
		}
		out := model.ConvCallbackOutput(chunk)
		if out == nil {
			continue
		}
		if out.Message != nil {
			result.messages = append(result.messages, out.Message)
			if result.firstToken.IsZero() && hasPayload(out.Message) {
				result.firstToken = time.Now()
			}
		}
		if out.TokenUsage != nil {
			result.usage = out.TokenUsage
		}
	}
}

// hasPayload 判断分片是否已携带模型可见内容，用于界定首个 token。
func hasPayload(message *schema.Message) bool {
	return message.Content != emptyValue ||
		message.ReasoningContent != emptyValue ||
		len(message.ToolCalls) > noItems
}

// rememberToolParents 记录 generation 声明的工具调用 ID 与其 span 上下文，
// 让随后执行的工具观测能挂到请求它的 generation 之下。
func rememberToolParents(ctx context.Context, message *schema.Message, span trace.Span) {
	rc := runContextFrom(ctx)
	if rc == nil || message == nil || len(message.ToolCalls) == noItems {
		return
	}
	for _, call := range message.ToolCalls {
		rc.rememberToolParent(call.ID, span.SpanContext())
	}
}

// observationAttrs 复制 trace 级属性并声明当前观测类型。
func observationAttrs(ctx context.Context, obsType string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, noItems, observationAttrCap)
	if rc := runContextFrom(ctx); rc != nil {
		attrs = append(attrs, rc.attributes()...)
	}
	return append(attrs, attribute.String(attrObsType, obsType))
}

// spanState 在 OnStart 与 OnEnd 之间传递 span 与开始时间。
type spanState struct {
	span    trace.Span
	started time.Time
}

type spanStateKey struct{}

func withSpanState(ctx context.Context, state *spanState) context.Context {
	return context.WithValue(ctx, spanStateKey{}, state)
}

func spanStateFrom(ctx context.Context) *spanState {
	state, ok := ctx.Value(spanStateKey{}).(*spanState)
	if !ok {
		return nil
	}
	return state
}
