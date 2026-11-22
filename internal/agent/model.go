// Package agent 为单次 run 执行 Eino ReAct 循环：模型轮次、工具调用、
// 需审批的副作用以及流式输出的助手回复。
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	goopenai "github.com/meguminnnnnnnnn/go-openai"

	"euler/internal/logging"
	"euler/internal/tools"
	"euler/internal/tracing"
)

// ErrModelRoundLimit 用于中止模型轮次预算耗尽的 run。
var ErrModelRoundLimit = errors.New("model round limit exceeded")

const (
	codeModelAuth        = "model_auth"
	codeModelRateLimited = "model_rate_limited"
	codeModelUnavailable = "model_unavailable"
	codeModelProtocol    = "model_protocol"
	codeModelRoundLimit  = "model_round_limit"
	codeModelTimeout     = "model_timeout"

	// defaultModelTimeout 是单次模型请求的空闲超时。
	defaultModelTimeout = 10 * time.Second
	// defaultModelRequestTimeout 是单次模型请求的总时长上限。
	defaultModelRequestTimeout = 5 * time.Minute

	maxModelRetries = 2
	firstAttempt    = 0
	singleModelCall = 1
	firstRetryWait  = 100 * time.Millisecond
	secondRetryWait = 200 * time.Millisecond

	// modelKeepAlive 是模型 HTTP 连接的 TCP keepalive 周期。
	modelKeepAlive = 30 * time.Second
	// noBytesRead 表示读取失败时没有返回任何字节。
	noBytesRead = 0

	noStatus           = 0
	statusUnauthorized = 401
	statusForbidden    = 403
	statusRateLimited  = 429
	statusServerError  = 500

	// noRoundLimit 是轮次上限的“不设限”取值，此时只受运行时长约束。
	noRoundLimit = 0

	zeroTimeout time.Duration = 0
)

// OpenAIChatModelFactory 构建真实的 OpenAI 兼容 chat 模型。
type OpenAIChatModelFactory struct {
	// Timeout 是单次模型请求的“空闲超时”：建连后任意一次读超过该时长仍无
	// 数据即失败，覆盖首 token 等待与流中途停顿。零值使用默认值。
	Timeout time.Duration
	// RequestTimeout 是单次模型请求的总时长上限，覆盖整个流式响应；
	// 零值使用默认值。
	RequestTimeout time.Duration
	// ReasoningEffort 非空时透传给模型，如 low/medium/high/max。
	ReasoningEffort string
}

// New 为指定模型参数创建一个 chat 模型。
func (f OpenAIChatModelFactory) New(
	ctx context.Context, spec ModelSpec,
) (model.ToolCallingChatModel, error) {
	idleTimeout := f.Timeout
	if idleTimeout <= zeroTimeout {
		idleTimeout = defaultModelTimeout
	}
	requestTimeout := f.RequestTimeout
	if requestTimeout <= zeroTimeout {
		requestTimeout = defaultModelRequestTimeout
	}
	config := &openai.ChatModelConfig{
		APIKey:  spec.APIKey,
		BaseURL: spec.BaseURL,
		Model:   spec.Model,
		// 用自定义 HTTPClient 实现空闲与总时长双重超时；设置后组件自身的
		// Timeout 不再生效。
		HTTPClient: newModelHTTPClient(idleTimeout, requestTimeout),
	}
	if f.ReasoningEffort != "" {
		// 通过 ExtraFields 透传，兼容 low/high/max 等厂商自有取值
		config.ExtraFields = map[string]any{
			"reasoning_effort": f.ReasoningEffort,
		}
	}
	chatModel, err := openai.NewChatModel(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create openai model: %w", err)
	}
	return chatModel, nil
}

// newModelHTTPClient 创建同时限制“空闲”和“总时长”的 HTTP 客户端：
// 任意一次读超过 idle 没有数据就失败；整个请求超过 total 也失败。
func newModelHTTPClient(idle, total time.Duration) *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Transport: http.DefaultTransport, Timeout: total}
	}
	transport := base.Clone()
	dialer := &net.Dialer{Timeout: idle, KeepAlive: modelKeepAlive}
	transport.DialContext = func(
		ctx context.Context, network, address string,
	) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &idleTimeoutConn{Conn: conn, idle: idle}, nil
	}
	transport.TLSHandshakeTimeout = idle
	return &http.Client{Transport: transport, Timeout: total}
}

// idleTimeoutConn 在每次读之前设置读截止时间，实现空闲超时。
type idleTimeoutConn struct {
	net.Conn
	idle time.Duration
}

// Read 在读取前刷新截止时间；TLS 握手和流式响应都走这里。
func (c *idleTimeoutConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.idle)); err != nil {
		return noBytesRead, err
	}
	return c.Conn.Read(p)
}

// roundLimitedModel 限制 Generate 和 Stream 的调用次数，覆盖 ReAct 图通过
// WithTools 派生的所有包装器；所有包装器共享同一个计数器。
// max 小于等于 0 表示不设限，仅受运行时长约束。
type roundLimitedModel struct {
	base  model.ToolCallingChatModel
	calls *atomic.Int64
	max   int
}

func newRoundLimitedModel(base model.ToolCallingChatModel, maxRounds int) *roundLimitedModel {
	return &roundLimitedModel{base: base, calls: &atomic.Int64{}, max: maxRounds}
}

// Generate 先消耗一轮预算再透传底层模型；预算耗尽返回 ErrModelRoundLimit。
func (m *roundLimitedModel) Generate(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	if err := m.count(ctx); err != nil {
		return nil, err
	}
	return m.base.Generate(ctx, input, opts...)
}

// Stream 先消耗一轮预算再透传底层模型；预算耗尽返回 ErrModelRoundLimit。
func (m *roundLimitedModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.count(ctx); err != nil {
		return nil, err
	}
	return m.base.Stream(ctx, input, opts...)
}

// WithTools 包装绑定工具后的模型，并让新包装器共享同一个轮次计数器。
func (m *roundLimitedModel) WithTools(
	infos []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	wrapped, err := m.base.WithTools(infos)
	if err != nil {
		return nil, err
	}
	return &roundLimitedModel{base: wrapped, calls: m.calls, max: m.max}, nil
}

func (m *roundLimitedModel) count(ctx context.Context) error {
	used := m.calls.Add(singleModelCall)
	if m.max > noRoundLimit && used > int64(m.max) {
		// 预算在模型真正发起请求前就已耗尽，需要显式写入追踪才能与模型失败区分
		tracing.RecordBudgetExhausted(ctx, int(used), m.max)
		// 用带错误码的包装保留 errors.Is 语义，同时让 run 层记录可归因的失败原因
		return &tools.CodedError{
			Code: codeModelRoundLimit,
			Msg:  ErrModelRoundLimit.Error(),
			Err:  ErrModelRoundLimit,
		}
	}
	return nil
}

// IsCallbacksEnabled 透传底层模型的自回调上报能力，避免 ReAct 图在已自带
// 回调的模型外再叠加一层回调，否则每轮的 stream 会被重复投递到 future 两次。
func (m *roundLimitedModel) IsCallbacksEnabled() bool {
	return components.IsCallbacksEnabled(m.base)
}

// retryModel 仅在初始 Generate/Stream 调用遇到网络错误、429 和 5xx 时重试，
// 最多重试两次，退避间隔为 100ms/200ms。其他失败均映射为带错误码且脱敏的
// 错误；context 错误原样透传。
type retryModel struct {
	base   model.ToolCallingChatModel
	apiKey string
}

func newRetryModel(base model.ToolCallingChatModel, apiKey string) *retryModel {
	return &retryModel{base: base, apiKey: apiKey}
}

// Generate 对可重试错误按退避重试；其余错误映射为带错误码且脱敏的错误。
func (m *retryModel) Generate(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	for attempt := firstAttempt; ; attempt++ {
		msg, err := m.base.Generate(ctx, input, opts...)
		if err == nil {
			return msg, nil
		}
		if !retryable(ctx, err, attempt) {
			return nil, m.mapError(ctx, err)
		}
		tracing.RecordRetry(ctx, attempt+firstAttempt+singleModelCall, err)
		if waitErr := waitRetry(ctx, attempt); waitErr != nil {
			return nil, waitErr
		}
	}
}

// Stream 对初始建流失败按退避重试；成功后的流中途错误只映射不重试。
func (m *retryModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	for attempt := firstAttempt; ; attempt++ {
		stream, err := m.base.Stream(ctx, input, opts...)
		if err == nil {
			return wrapStreamErrors(ctx, stream, m.apiKey), nil
		}
		if !retryable(ctx, err, attempt) {
			return nil, m.mapError(ctx, err)
		}
		tracing.RecordRetry(ctx, attempt+firstAttempt+singleModelCall, err)
		if waitErr := waitRetry(ctx, attempt); waitErr != nil {
			return nil, waitErr
		}
	}
}

// WithTools 包装绑定工具后的模型，使派生模型保留同样的重试与脱敏语义。
func (m *retryModel) WithTools(
	infos []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	wrapped, err := m.base.WithTools(infos)
	if err != nil {
		return nil, err
	}
	return newRetryModel(wrapped, m.apiKey), nil
}

// mapError 区分“run 截止”与“单次模型请求超时”：前者保留 context 错误，
// 后者映射为 model_timeout，避免归因成 run_timeout。
func (m *retryModel) mapError(ctx context.Context, err error) error {
	if ctx.Err() == nil && isModelTimeout(err) {
		return &tools.CodedError{
			Code: codeModelTimeout,
			Msg:  "model request timed out",
			Err:  err,
		}
	}
	if isContextError(err) {
		return err
	}
	return mapModelError(err, m.apiKey)
}

// IsCallbacksEnabled 透传底层模型的自回调上报能力；参见
// roundLimitedModel.IsCallbacksEnabled。
func (m *retryModel) IsCallbacksEnabled() bool {
	return components.IsCallbacksEnabled(m.base)
}

// wrapStreamErrors 映射流中途的失败但不重试；context 错误原样透传，
// 使取消操作保持其 run 语义。
func wrapStreamErrors(
	ctx context.Context,
	stream *schema.StreamReader[*schema.Message],
	apiKey string,
) *schema.StreamReader[*schema.Message] {
	return schema.StreamReaderWithConvert(stream,
		func(msg *schema.Message) (*schema.Message, error) { return msg, nil },
		schema.WithErrWrapper(func(err error) error {
			if ctx.Err() == nil && isModelTimeout(err) {
				return &tools.CodedError{
					Code: codeModelTimeout,
					Msg:  "model stream timed out",
					Err:  err,
				}
			}
			if isContextError(err) {
				return err
			}
			return mapModelError(err, apiKey)
		}))
}

func retryable(ctx context.Context, err error, attempt int) bool {
	if attempt >= maxModelRetries || ctx.Err() != nil || isContextError(err) {
		return false
	}
	if status, ok := httpStatus(err); ok {
		return status == statusRateLimited || status >= statusServerError
	}
	return networkError(err)
}

func waitRetry(ctx context.Context, attempt int) error {
	wait := firstRetryWait
	if attempt > firstAttempt {
		wait = secondRetryWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// mapModelError 将模型失败归类为稳定的 run 错误码，并在错误信息进入
// 存储或日志之前脱敏其中的 API key。
func mapModelError(err error, apiKey string) error {
	code := codeModelProtocol
	if status, ok := httpStatus(err); ok {
		code = statusErrorCode(status)
	} else if isStreamTimeout(err) {
		code = codeModelTimeout
	} else if networkError(err) {
		code = codeModelUnavailable
	}
	return &tools.CodedError{
		Code: code,
		Msg:  logging.Redact(err.Error(), apiKey),
		Err:  err,
	}
}

func statusErrorCode(status int) string {
	switch {
	case status == statusUnauthorized || status == statusForbidden:
		return codeModelAuth
	case status == statusRateLimited:
		return codeModelRateLimited
	case status >= statusServerError:
		return codeModelUnavailable
	default:
		return codeModelProtocol
	}
}

func httpStatus(err error) (int, bool) {
	// eino-ext 的 OpenAI 包装器会把 go-openai 的 API 错误改写为自己的
	// APIError 类型且没有 Unwrap 链，因此先检查它。
	var extErr *openai.APIError
	if errors.As(err, &extErr) {
		return extErr.HTTPStatusCode, true
	}
	var apiErr *goopenai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode, true
	}
	var reqErr *goopenai.RequestError
	if errors.As(err, &reqErr) {
		return reqErr.HTTPStatusCode, true
	}
	return noStatus, false
}

// isModelTimeout 判断是否为单次模型请求超时：context 截止或 socket 空闲超时。
func isModelTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || isStreamTimeout(err)
}

// isStreamTimeout 判断是否为 socket 读超时；它对应空闲超时，不应归类为
// 网络不可用或协议错误。
func isStreamTimeout(err error) bool {
	var netErr net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout())
}

func networkError(err error) bool {
	var urlErr *url.Error
	var netErr net.Error
	return errors.Is(err, io.EOF) || errors.As(err, &urlErr) || errors.As(err, &netErr)
}
