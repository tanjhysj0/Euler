// Package agent runs the Eino ReAct loop for one run: model rounds, tool
// calls, approval gated side effects and streamed assistant output.
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
	"euler/internal/modelprofile"
	"euler/internal/tools"
)

// ErrModelRoundLimit aborts a run whose model round budget is exhausted.
var ErrModelRoundLimit = errors.New("model round limit exceeded")

const (
	codeModelAuth        = "model_auth"
	codeModelRateLimited = "model_rate_limited"
	codeModelUnavailable = "model_unavailable"
	codeModelProtocol    = "model_protocol"

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

// OpenAIChatModelFactory builds real OpenAI compatible chat models.
type OpenAIChatModelFactory struct{}

// New creates one chat model for profile.
func (OpenAIChatModelFactory) New(
	ctx context.Context, profile modelprofile.ResolvedProfile,
) (model.ToolCallingChatModel, error) {
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  profile.APIKey,
		BaseURL: profile.BaseURL,
		Model:   profile.Model,
		Timeout: 60 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create openai model: %w", err)
	}
	return chatModel, nil
}

// roundLimitedModel caps Generate and Stream calls across every wrapper the
// ReAct graph derives through WithTools; all wrappers share one counter.
type roundLimitedModel struct {
	base  model.ToolCallingChatModel
	calls *atomic.Int64
	max   int
}

func newRoundLimitedModel(base model.ToolCallingChatModel, maxRounds int) *roundLimitedModel {
	return &roundLimitedModel{base: base, calls: &atomic.Int64{}, max: maxRounds}
}

func (m *roundLimitedModel) Generate(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	if err := m.count(); err != nil {
		return nil, err
	}
	return m.base.Generate(ctx, input, opts...)
}

func (m *roundLimitedModel) Stream(
	ctx context.Context, input []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := m.count(); err != nil {
		return nil, err
	}
	return m.base.Stream(ctx, input, opts...)
}

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
		return ErrModelRoundLimit
	}
	return nil
}

// IsCallbacksEnabled forwards the base model's self-callback reporting so the
// ReAct graph does not add a second callback layer over a model that already
// emits its own, which would deliver every round stream to the future twice.
func (m *roundLimitedModel) IsCallbacksEnabled() bool {
	return components.IsCallbacksEnabled(m.base)
}

// retryModel retries only the initial Generate/Stream call on network errors,
// 429 and 5xx, twice with 100ms/200ms backoff. Every other failure is mapped
// to a coded, redacted error; context errors pass through untouched.
type retryModel struct {
	base   model.ToolCallingChatModel
	apiKey string
}

func newRetryModel(base model.ToolCallingChatModel, apiKey string) *retryModel {
	return &retryModel{base: base, apiKey: apiKey}
}

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

// IsCallbacksEnabled forwards the base model's self-callback reporting; see
// roundLimitedModel.IsCallbacksEnabled.
func (m *retryModel) IsCallbacksEnabled() bool {
	return components.IsCallbacksEnabled(m.base)
}

// wrapStreamErrors maps mid-stream failures without retrying them; context
// errors pass through so cancellation keeps its run semantics.
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

// mapModelError classifies a model failure into a stable run error code and
// redacts the API key from the message before it can reach storage or logs.
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
	// The eino-ext OpenAI wrapper rewrites go-openai API errors into its own
	// APIError type without an Unwrap chain, so check it first.
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
