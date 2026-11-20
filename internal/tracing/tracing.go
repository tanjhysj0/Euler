// Package tracing 通过 OpenTelemetry OTLP 把 agent 执行链路上报到 Langfuse。
// 每次 run 对应一条 trace，根观测类型为 agent，模型调用为 generation，
// 工具调用为 tool，并携带 session、输入输出和 token 用量。
package tracing

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"euler/internal/logging"
)

const (
	instrumentationName = "euler"
	runSpanName         = "run-coding-agent"
	otelTracesPath      = "/api/public/otel/v1/traces"
	defaultEnvironment  = "local"
	flushTimeout        = 5 * time.Second
	batchTimeout        = time.Second
	maxQueueSize        = 2048

	authHeader           = "Authorization"
	ingestionHeader      = "x-langfuse-ingestion-version"
	ingestionVersion     = "4"
	attributeSourceName  = "service.name"
	attributeSourceValue = "euler"
	traceMetaRunID       = "run_id"
	traceMetaWorkspace   = "workspace"

	// 以下元数据键用于暴露重试与预算等执行细节。
	traceMetaRetryCount      = "retry_count"
	traceMetaRetryError      = "retry_last_error"
	traceMetaBudgetExhausted = "budget_exhausted"
	traceMetaBudgetUsed      = "budget_used"
	traceMetaBudgetLimit     = "budget_limit"
)

// Config 是连接 Langfuse 所需的参数。
type Config struct {
	Host        string
	PublicKey   string
	SecretKey   string
	Environment string
}

// TraceMeta 描述单次 run 的追踪属性，决定 Langfuse 中的检索维度。
type TraceMeta struct {
	RunID     uuid.UUID
	SessionID uuid.UUID
	Input     string
	Workspace string
	Model     string
	APIKey    string
}

// Tracer 通过 OTel SDK 创建 trace，并把 Eino 回调映射为 Langfuse 观测。
type Tracer struct {
	provider    *sdktrace.TracerProvider
	tracer      trace.Tracer
	environment string
}

// New 构建 OTLP 导出器并注册 Eino 全局回调处理器。
// 连接参数不合法时返回错误，由调用方决定是否降级为关闭追踪。
func New(cfg Config) (*Tracer, error) {
	headers := map[string]string{
		authHeader:      "Basic " + basicAuth(cfg.PublicKey, cfg.SecretKey),
		ingestionHeader: ingestionVersion,
	}
	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(cfg.Host+otelTracesPath),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithTimeout(flushTimeout),
	)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(batchTimeout),
			sdktrace.WithMaxQueueSize(maxQueueSize),
		),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String(attributeSourceName, attributeSourceValue),
		)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	environment := cfg.Environment
	if environment == emptyValue {
		environment = defaultEnvironment
	}
	return newTracer(provider, environment), nil
}

// newTracer 在给定 provider 上注册回调处理器，便于测试注入内存导出器。
func newTracer(provider *sdktrace.TracerProvider, environment string) *Tracer {
	tracer := provider.Tracer(instrumentationName)
	callbacks.AppendGlobalHandlers(newHandler(tracer))
	return &Tracer{provider: provider, tracer: tracer, environment: environment}
}

// StartRun 为一次 run 开启根观测；根观测类型为 agent，承载用户输入。
// 输入装载完成前调用时 Input/Model/Workspace 可以为空，
// 后续用 AnnotateRun 补齐，使装载阶段也处于 trace 之内。
func (t *Tracer) StartRun(ctx context.Context, meta TraceMeta) context.Context {
	redact := func(text string) string { return logging.Redact(text, meta.APIKey) }
	attrs := traceAttributes(meta, t.environment)
	rootAttrs := make([]attribute.KeyValue, noItems, len(attrs)+observationAttrCap)
	rootAttrs = append(rootAttrs, attrs...)
	rootAttrs = append(rootAttrs, attribute.String(attrObsType, obsTypeAgent))
	if meta.Input != emptyValue {
		rootAttrs = append(rootAttrs, attribute.String(
			attrObsInput, redact(jsonText(meta.Input)),
		))
	}
	ctx, _ = t.tracer.Start(ctx, runSpanName,
		trace.WithNewRoot(),
		trace.WithAttributes(rootAttrs...),
	)
	return context.WithValue(ctx, runContextKey{}, &runContext{
		attrs: attrs, model: meta.Model, redact: redact,
	})
}

// AnnotateRun 在输入装载后补齐根观测的输入与检索维度，
// 并更新后续子观测复制的属性与脱敏函数。
func (t *Tracer) AnnotateRun(ctx context.Context, meta TraceMeta) {
	redact := func(text string) string { return logging.Redact(text, meta.APIKey) }
	attrs := traceAttributes(meta, t.environment)
	if meta.Input != emptyValue {
		attrs = append(attrs, attribute.String(
			attrObsInput, redact(jsonText(meta.Input)),
		))
	}
	trace.SpanFromContext(ctx).SetAttributes(attrs...)
	if rc := runContextFrom(ctx); rc != nil {
		rc.annotate(traceAttributes(meta, t.environment), meta.Model, redact)
	}
}

// StartSpan 在当前 trace 下创建一个普通子观测，用于装载、构建等非模型步骤。
// 返回的收尾函数在 err 非空时把该观测标记为失败。
func (t *Tracer) StartSpan(
	ctx context.Context, name string,
) (context.Context, func(error)) {
	attrs := observationAttrs(ctx, obsTypeSpan)
	ctx, span := t.tracer.Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, func(err error) {
		if err != nil {
			markSpanError(ctx, span, err)
		}
		span.End()
	}
}

// SetOutput 写入本次 run 的最终助手输出；由 agent 在生成结束后调用，
// 与根观测的结束解耦，使 trace 生命周期可以留在 run 层。
func (*Tracer) SetOutput(ctx context.Context, output string) {
	if output == emptyValue {
		return
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String(
		attrObsOutput, redactorFrom(ctx)(jsonText(output)),
	))
}

// FinishRun 结束根观测：失败时标记 ERROR 并记录原因。
func (*Tracer) FinishRun(ctx context.Context, errMsg string) {
	span := trace.SpanFromContext(ctx)
	if errMsg != emptyValue {
		reason := redactorFrom(ctx)(errMsg)
		span.SetAttributes(
			attribute.String(attrObsLevel, levelError),
			attribute.String(attrObsStatus, reason),
		)
		span.RecordError(errors.New(reason))
		span.SetStatus(codes.Error, reason)
	}
	span.End()
}

// RunActive 报告 ctx 中是否已有活跃的 run 级 trace，
// 供 agent 避免在 run 层已建 trace 时重复创建根观测。
func (*Tracer) RunActive(ctx context.Context) bool {
	return runContextFrom(ctx) != nil
}

// RecordRetry 记录模型调用发生的一次重试。
// 重试发生在同一个 generation 内部，不写这条属性就无法与正常调用区分。
func RecordRetry(ctx context.Context, attempt int, err error) {
	state := spanStateFrom(ctx)
	if state == nil || err == nil {
		return
	}
	state.span.SetAttributes(
		attribute.Int(attrObsMetaPrefix+traceMetaRetryCount, attempt),
		attribute.String(
			attrObsMetaPrefix+traceMetaRetryError, redact(ctx, err.Error()),
		),
	)
}

// RecordBudgetExhausted 标记本次模型调用的轮次预算已耗尽。
// 该失败发生在模型真正发起请求之前，需要显式记录才能定位到预算原因。
func RecordBudgetExhausted(ctx context.Context, used, limit int) {
	state := spanStateFrom(ctx)
	if state == nil {
		return
	}
	state.span.SetAttributes(
		attribute.Bool(attrObsMetaPrefix+traceMetaBudgetExhausted, true),
		attribute.Int(attrObsMetaPrefix+traceMetaBudgetUsed, used),
		attribute.Int(attrObsMetaPrefix+traceMetaBudgetLimit, limit),
	)
}

// Flush 在进程退出前强制刷新并关闭导出器，避免丢失最后一批 span。
func (t *Tracer) Flush() {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	if err := t.provider.Shutdown(ctx); err != nil {
		slog.Warn("shutdown langfuse tracer", "error", err)
	}
}

// traceAttributes 返回需要复制到每个 span 的 trace 级属性。
func traceAttributes(meta TraceMeta, environment string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(attrTraceName, runSpanName),
		attribute.String(attrSessionID, meta.SessionID.String()),
		attribute.String(attrEnvironment, environment),
		attribute.StringSlice(attrTraceTags, []string{"euler", "coding-agent"}),
		attribute.String(attrTraceMetaPrefix+traceMetaRunID, meta.RunID.String()),
		attribute.String(attrTraceMetaPrefix+traceMetaWorkspace, meta.Workspace),
	}
}

// basicAuth 生成 Langfuse OTLP 端点要求的 Basic 认证串。
func basicAuth(publicKey, secretKey string) string {
	pair := publicKey + ":" + secretKey
	return base64.StdEncoding.EncodeToString([]byte(pair))
}

// runContext 携带需要在所有子 span 上复制的属性与脱敏函数，
// 并记录本次 run 中各工具调用所属的 generation，供工具观测挂接父级。
type runContext struct {
	attrs  []attribute.KeyValue
	model  string
	redact func(string) string

	mu             sync.Mutex
	toolParents    map[string]trace.SpanContext
	lastGeneration trace.SpanContext
}

// annotate 用装载完成后的完整信息替换 run 级属性与脱敏函数。
func (rc *runContext) annotate(
	attrs []attribute.KeyValue, model string, redact func(string) string,
) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.attrs = attrs
	rc.model = model
	rc.redact = redact
}

// attributes 返回 run 级属性的副本，供子观测复制时安全读取。
func (rc *runContext) attributes() []attribute.KeyValue {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]attribute.KeyValue(nil), rc.attrs...)
}

// rememberGeneration 记录本次 run 最近开始的 generation，
// 供随后执行的工具观测同步取用父级，避免依赖流式输出消费时序。
func (rc *runContext) rememberGeneration(generation trace.SpanContext) {
	if !generation.IsValid() {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.lastGeneration = generation
}

// rememberToolParent 记录某次工具调用由哪个 generation 发起。
func (rc *runContext) rememberToolParent(callID string, parent trace.SpanContext) {
	if callID == emptyValue || !parent.IsValid() {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.toolParents == nil {
		rc.toolParents = map[string]trace.SpanContext{}
	}
	rc.toolParents[callID] = parent
}

// generationParent 返回工具调用应挂接的 generation：优先按调用 ID 精确匹配，
// 否则回退到最近开始的 generation。
func (rc *runContext) generationParent(callID string) (trace.SpanContext, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if parent, ok := rc.toolParents[callID]; ok {
		delete(rc.toolParents, callID)
		return parent, true
	}
	if rc.lastGeneration.IsValid() {
		return rc.lastGeneration, true
	}
	return trace.SpanContext{}, false
}

type runContextKey struct{}

func runContextFrom(ctx context.Context) *runContext {
	value, ok := ctx.Value(runContextKey{}).(*runContext)
	if !ok {
		return nil
	}
	return value
}

func redactorFrom(ctx context.Context) func(string) string {
	if rc := runContextFrom(ctx); rc != nil && rc.redact != nil {
		return rc.redact
	}
	return func(text string) string { return logging.Redact(text) }
}

func redact(ctx context.Context, text string) string {
	return redactorFrom(ctx)(text)
}
