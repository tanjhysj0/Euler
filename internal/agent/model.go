// Package agent 为单次 run 执行 Eino ReAct 循环：模型轮次、工具调用、
// 需审批的副作用以及流式输出的助手回复。
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	goopenai "github.com/meguminnnnnnnnn/go-openai"

	"euler/internal/logging"
	"euler/internal/tools"
)

// ErrModelRoundLimit 用于中止模型轮次预算耗尽的 run。
var ErrModelRoundLimit = errors.New("model round limit exceeded")

const (
	codeModelAuth        = "model_auth"
	codeModelRateLimited = "model_rate_limited"
	codeModelUnavailable = "model_unavailable"
	codeModelProtocol    = "model_protocol"
	codeModelRoundLimit  = "model_round_limit"

	maxModelRetries = 2
	firstAttempt    = 0
	singleModelCall = 1
	firstRetryWait  = 100 * time.Millisecond
	secondRetryWait = 200 * time.Millisecond

	noStatus           = 0
	statusUnauthorized = 401
	statusForbidden    = 403
	statusRateLimited  = 429
	statusServerError  = 500
)

// OpenAIChatModelFactory 构建真实的 OpenAI 兼容 chat 模型。
type OpenAIChatModelFactory struct{}

// New 为指定模型参数创建一个 chat 模型。
func (OpenAIChatModelFactory) New(
	ctx context.Context, spec ModelSpec,
) (model.ToolCallingChatModel, error) {
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  spec.APIKey,
		BaseURL: spec.BaseURL,
		Model:   spec.Model,
		Timeout: 60 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create openai model: %w", err)
	}
	return chatModel, nil
}

// roundLimitedModel 限制 Generate 和 Stream 的调用次数，覆盖 ReAct 图通过
// WithTools 派生的所有包装器；所有包装器共享同一个计数器。
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
	if err := m.count(); err != nil {
		return nil, err
	}
	return m.base.Generate(ctx, input, opts...)
}

// Stream 先消耗一轮预算再透传底层模型；预算耗尽返回 ErrModelRoundLimit。
func (m *roundLimitedModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.count(); err != nil {
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

func (m *roundLimitedModel) count() error {
	if m.calls.Add(singleModelCall) > int64(m.max) {
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
			return nil, m.mapError(err)
		}
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
			return wrapStreamErrors(stream, m.apiKey), nil
		}
		if !retryable(ctx, err, attempt) {
			return nil, m.mapError(err)
		}
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

func (m *retryModel) mapError(err error) error {
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
	stream *schema.StreamReader[*schema.Message], apiKey string,
) *schema.StreamReader[*schema.Message] {
	return schema.StreamReaderWithConvert(stream,
		func(msg *schema.Message) (*schema.Message, error) { return msg, nil },
		schema.WithErrWrapper(func(err error) error {
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

func networkError(err error) bool {
	var urlErr *url.Error
	var netErr net.Error
	return errors.Is(err, io.EOF) || errors.As(err, &urlErr) || errors.As(err, &netErr)
}
