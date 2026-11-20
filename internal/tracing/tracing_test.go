package tracing

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	testEnvironment = "test"
	testModel       = "deepseek-chat"
	testToolCallID  = "call-1"
)

// newTestTracer 返回内存导出器上的追踪器，避免测试依赖网络。
func newTestTracer(t *testing.T) (*Tracer, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown provider: %v", err)
		}
	})
	tracer := &Tracer{
		provider: provider, tracer: provider.Tracer(instrumentationName),
		environment: testEnvironment,
	}
	return tracer, exporter
}

// testIDs 保存一次测试 run 的标识。
type testIDs struct {
	runID     uuid.UUID
	sessionID uuid.UUID
}

// startTestRun 开启一次 run 并返回根上下文与 run/session ID。
func startTestRun(t *testing.T, tracer *Tracer) (context.Context, testIDs) {
	t.Helper()
	runID, sessionID := uuid.New(), uuid.New()
	ctx := tracer.StartRun(context.Background(), TraceMeta{
		RunID: runID, SessionID: sessionID, Input: "读一下 note.txt",
		Workspace: "/tmp/workspace", Model: testModel, APIKey: "sk-secret",
	})
	return ctx, testIDs{runID: runID, sessionID: sessionID}
}

func TestStartRunBuildsAgentRootSpan(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx, ids := startTestRun(t, tracer)
	tracer.SetOutput(ctx, "已读取")
	tracer.FinishRun(ctx, "")

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	require.Equal(t, runSpanName, span.Name)
	require.Equal(t, obsTypeAgent, attrValue(span, attrObsType))
	require.Equal(t, ids.sessionID.String(), attrValue(span, attrSessionID))
	require.Equal(t, testEnvironment, attrValue(span, attrEnvironment))
	require.Equal(t, `"读一下 note.txt"`, attrValue(span, attrObsInput))
	require.Equal(t, `"已读取"`, attrValue(span, attrObsOutput))
	require.Equal(t, ids.runID.String(), attrValue(span, attrTraceMetaPrefix+traceMetaRunID))
}

// startRunContext 只返回根上下文，供回调测试使用。
func startRunContext(t *testing.T, tracer *Tracer) context.Context {
	t.Helper()
	ctx, _ := startTestRun(t, tracer)
	return ctx
}

func TestHandlerMapsStreamedGeneration(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	h := newHandler(tracer.tracer)
	ctx := startRunContext(t, tracer)
	info := &callbacks.RunInfo{Component: components.ComponentOfChatModel}
	ctx = h.OnStart(ctx, info, &model.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("读一下 note.txt")},
		Config:   &model.Config{Model: testModel, Temperature: 0.3},
	})
	ctx = h.OnEndWithStreamOutput(ctx, info, schema.StreamReaderFromArray(
		[]callbacks.CallbackOutput{
			&model.CallbackOutput{Message: schema.AssistantMessage("正在", nil)},
			&model.CallbackOutput{
				Message: schema.AssistantMessage("读取", nil),
				TokenUsage: &model.TokenUsage{
					PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
				},
			},
		},
	))
	require.Eventually(t, func() bool { return len(exporter.GetSpans()) > noItems },
		time.Second, 10*time.Millisecond)
	tracer.SetOutput(ctx, "已读取")
	tracer.FinishRun(ctx, "")

	generation := findSpan(t, exporter.GetSpans(), spanGenerateResponse)
	require.Equal(t, obsTypeGeneration, attrValue(generation, attrObsType))
	require.Equal(t, testModel, attrValue(generation, attrObsModel))
	require.Contains(t, attrValue(generation, attrObsInput), "读一下 note.txt")
	require.Contains(t, attrValue(generation, attrObsOutput), "读取")
	require.JSONEq(t, `{"input":10,"output":2,"total":12}`,
		attrValue(generation, attrObsUsage))
	require.NotEmpty(t, attrValue(generation, attrObsCompletion))
}

func TestHandlerMapsToolCall(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	h := newHandler(tracer.tracer)
	ctx := startRunContext(t, tracer)
	info := &callbacks.RunInfo{Component: components.ComponentOfTool, Name: "read_file"}
	ctx = h.OnStart(ctx, info, &tool.CallbackInput{
		ArgumentsInJSON: `{"path":"note.txt"}`,
	})
	ctx = h.OnEnd(ctx, info, &tool.CallbackOutput{Response: `{"status":"completed"}`})
	tracer.SetOutput(ctx, "已读取")
	tracer.FinishRun(ctx, "")

	span := findSpan(t, exporter.GetSpans(), "read_file")
	require.Equal(t, obsTypeTool, attrValue(span, attrObsType))
	require.Equal(t, `{"path":"note.txt"}`, attrValue(span, attrObsInput))
	require.Equal(t, `{"status":"completed"}`, attrValue(span, attrObsOutput))
	require.True(t, span.Parent.IsValid())
}

func TestHandlerMarksErrors(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	h := newHandler(tracer.tracer)
	ctx := startRunContext(t, tracer)
	info := &callbacks.RunInfo{Component: components.ComponentOfChatModel}
	ctx = h.OnStart(ctx, info, &model.CallbackInput{Messages: []*schema.Message{
		schema.UserMessage("hi"),
	}})
	ctx = h.OnError(ctx, info, errors.New("model auth failed: sk-secret"))
	tracer.FinishRun(ctx, "model auth failed")

	span := findSpan(t, exporter.GetSpans(), spanGenerateResponse)
	require.Equal(t, levelError, attrValue(span, attrObsLevel))
	require.Equal(t, "model auth failed: [REDACTED]", attrValue(span, attrObsStatus))
	require.Equal(t, codes.Error, span.Status.Code)
}

func TestGenerationProvidesToolParent(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	h := newHandler(tracer.tracer)
	ctx := startRunContext(t, tracer)
	info := &callbacks.RunInfo{Component: components.ComponentOfChatModel}
	ctx = h.OnStart(ctx, info, &model.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("读一下 note.txt")},
	})
	ctx = h.OnEnd(ctx, info, &model.CallbackOutput{Message: schema.AssistantMessage(
		"", []schema.ToolCall{{ID: testToolCallID, Function: schema.FunctionCall{
			Name: "read_file", Arguments: `{"path":"note.txt"}`,
		}}},
	)})

	rc := runContextFrom(ctx)
	require.NotNil(t, rc)
	generation := findSpan(t, exporter.GetSpans(), spanGenerateResponse)
	parent, ok := rc.generationParent(testToolCallID)
	require.True(t, ok)
	require.Equal(t, generation.SpanContext.SpanID(), parent.SpanID())

	// 精确条目取用后，后续工具仍回退到最近一次 generation
	fallback, ok := rc.generationParent("call-unknown")
	require.True(t, ok)
	require.Equal(t, parent.SpanID(), fallback.SpanID())
}

func TestGenerationParentAbsentWithoutGeneration(t *testing.T) {
	rc := &runContext{}
	_, ok := rc.generationParent(testToolCallID)
	require.False(t, ok)
}

func TestStreamErrorMarksGeneration(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	h := newHandler(tracer.tracer)
	ctx := startRunContext(t, tracer)
	info := &callbacks.RunInfo{Component: components.ComponentOfChatModel}
	ctx = h.OnStart(ctx, info, &model.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("hi")},
	})
	reader, writer := schema.Pipe[callbacks.CallbackOutput](1)
	go func() {
		writer.Send(&model.CallbackOutput{Message: schema.AssistantMessage("部分", nil)}, nil)
		writer.Send(nil, errors.New("stream broke: sk-secret"))
		writer.Close()
	}()
	ctx = h.OnEndWithStreamOutput(ctx, info, reader)
	require.Eventually(t, func() bool { return len(exporter.GetSpans()) > noItems },
		time.Second, 10*time.Millisecond)

	generation := findSpan(t, exporter.GetSpans(), spanGenerateResponse)
	require.Equal(t, levelError, attrValue(generation, attrObsLevel))
	require.Contains(t, attrValue(generation, attrObsStatus), "stream broke")
	require.Contains(t, attrValue(generation, attrObsStatus), "[REDACTED]")
	require.Equal(t, codes.Error, generation.Status.Code)
	tracer.FinishRun(ctx, "")
}

func TestAnnotateRunFillsInputAndWorkspace(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ids := testIDs{runID: uuid.New(), sessionID: uuid.New()}
	ctx := tracer.StartRun(context.Background(), TraceMeta{
		RunID: ids.runID, SessionID: ids.sessionID,
	})
	tracer.AnnotateRun(ctx, TraceMeta{
		RunID: ids.runID, SessionID: ids.sessionID, Input: "列出文件",
		Workspace: "/tmp/ws", Model: testModel, APIKey: "sk-secret",
	})
	tracer.SetOutput(ctx, "完成")
	tracer.FinishRun(ctx, "")

	span := findSpan(t, exporter.GetSpans(), runSpanName)
	require.Equal(t, `"列出文件"`, attrValue(span, attrObsInput))
	require.Equal(t, "/tmp/ws", attrValue(span, attrTraceMetaPrefix+traceMetaWorkspace))
}

func TestStartSpanMarksError(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	ctx := tracer.StartRun(context.Background(), TraceMeta{
		RunID: uuid.New(), SessionID: uuid.New(), APIKey: "sk-secret",
	})
	_, end := tracer.StartSpan(ctx, "load-run-input")
	end(errors.New("db down: sk-secret"))
	tracer.FinishRun(ctx, "db down")

	span := findSpan(t, exporter.GetSpans(), "load-run-input")
	require.Equal(t, obsTypeSpan, attrValue(span, attrObsType))
	require.Equal(t, levelError, attrValue(span, attrObsLevel))
	require.Contains(t, attrValue(span, attrObsStatus), "[REDACTED]")
	require.True(t, span.Parent.IsValid())
}

func TestRecordRetryWritesMetadata(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	h := newHandler(tracer.tracer)
	ctx := startRunContext(t, tracer)
	info := &callbacks.RunInfo{Component: components.ComponentOfChatModel}
	ctx = h.OnStart(ctx, info, &model.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("hi")},
	})
	RecordRetry(ctx, 1, errors.New("429 too many requests: sk-secret"))
	ctx = h.OnEnd(ctx, info, &model.CallbackOutput{
		Message: schema.AssistantMessage("好", nil),
	})
	tracer.FinishRun(ctx, "")

	generation := findSpan(t, exporter.GetSpans(), spanGenerateResponse)
	require.Equal(t, "1",
		attrValue(generation, attrObsMetaPrefix+traceMetaRetryCount))
	require.Contains(t,
		attrValue(generation, attrObsMetaPrefix+traceMetaRetryError), "[REDACTED]")
}

func TestRecordBudgetExhaustedWritesMetadata(t *testing.T) {
	tracer, exporter := newTestTracer(t)
	h := newHandler(tracer.tracer)
	ctx := startRunContext(t, tracer)
	info := &callbacks.RunInfo{Component: components.ComponentOfChatModel}
	ctx = h.OnStart(ctx, info, &model.CallbackInput{
		Messages: []*schema.Message{schema.UserMessage("hi")},
	})
	RecordBudgetExhausted(ctx, 3, 2)
	ctx = h.OnEnd(ctx, info, &model.CallbackOutput{
		Message: schema.AssistantMessage("好", nil),
	})
	tracer.FinishRun(ctx, "")

	generation := findSpan(t, exporter.GetSpans(), spanGenerateResponse)
	require.Equal(t, "true",
		attrValue(generation, attrObsMetaPrefix+traceMetaBudgetExhausted))
	require.Equal(t, "3",
		attrValue(generation, attrObsMetaPrefix+traceMetaBudgetUsed))
	require.Equal(t, "2",
		attrValue(generation, attrObsMetaPrefix+traceMetaBudgetLimit))
}

func attrValue(span tracetest.SpanStub, key string) string {
	for _, kv := range span.Attributes {
		if string(kv.Key) != key {
			continue
		}
		switch kv.Value.Type() { //nolint:exhaustive // 只区分整数、布尔与字符串
		case attribute.INT64:
			return strconv.FormatInt(kv.Value.AsInt64(), 10)
		case attribute.BOOL:
			return strconv.FormatBool(kv.Value.AsBool())
		default:
			return kv.Value.AsString()
		}
	}
	return emptyValue
}

func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("span %q not found", name)
	return tracetest.SpanStub{}
}
