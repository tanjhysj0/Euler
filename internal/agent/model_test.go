package agent

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	goopenai "github.com/meguminnnnnnnnn/go-openai"
	"github.com/stretchr/testify/require"

	"euler/internal/tools"
)

func TestRoundLimitedModelStopsExcessiveCalls(t *testing.T) {
	base := &fakeToolCallingModel{}
	limited := newRoundLimitedModel(base, 1)
	input := []*schema.Message{schema.UserMessage("hello")}
	_, err := limited.Generate(context.Background(), input)
	require.NoError(t, err)
	_, err = limited.Generate(context.Background(), input)
	require.ErrorIs(t, err, ErrModelRoundLimit)
	var coded interface{ ErrorCode() string }
	require.ErrorAs(t, err, &coded)
	require.Equal(t, codeModelRoundLimit, coded.ErrorCode())
	require.Equal(t, 1, base.generateCalls)
}

func TestRoundLimitedModelAllowsUnlimitedRounds(t *testing.T) {
	base := &fakeToolCallingModel{}
	limited := newRoundLimitedModel(base, 0)
	input := []*schema.Message{schema.UserMessage("hello")}
	for range 50 {
		_, err := limited.Generate(context.Background(), input)
		require.NoError(t, err)
	}
	require.Equal(t, 50, base.generateCalls)
}

func TestRoundLimitedModelSharesCounterAcrossWithTools(t *testing.T) {
	base := &fakeToolCallingModel{}
	limited := newRoundLimitedModel(base, 2)
	wrapped, err := limited.WithTools(nil)
	require.NoError(t, err)
	require.Equal(t, 1, base.withToolsCalls)
	input := []*schema.Message{schema.UserMessage("hello")}
	_, err = limited.Generate(context.Background(), input)
	require.NoError(t, err)
	_, err = wrapped.Generate(context.Background(), input)
	require.NoError(t, err)
	_, err = wrapped.Generate(context.Background(), input)
	require.ErrorIs(t, err, ErrModelRoundLimit)
	require.Equal(t, 2, base.generateCalls)
}

func TestRoundLimitedModelCountsStreamCalls(t *testing.T) {
	base := &fakeToolCallingModel{}
	limited := newRoundLimitedModel(base, 1)
	input := []*schema.Message{schema.UserMessage("hello")}
	stream, err := limited.Stream(context.Background(), input)
	require.NoError(t, err)
	stream.Close()
	_, err = limited.Stream(context.Background(), input)
	require.ErrorIs(t, err, ErrModelRoundLimit)
	require.Equal(t, 1, base.streamCalls)
}

func TestRetryModelMapsClientTimeoutToModelTimeoutCode(t *testing.T) {
	base := &fakeToolCallingModel{generateErrs: []error{context.DeadlineExceeded}}
	retrying := newRetryModel(base, "key")
	_, err := retrying.Generate(
		context.Background(), []*schema.Message{schema.UserMessage("hello")},
	)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var coded interface{ ErrorCode() string }
	require.ErrorAs(t, err, &coded)
	require.Equal(t, codeModelTimeout, coded.ErrorCode())
}

func TestStreamTimeoutMapsToModelTimeoutCode(t *testing.T) {
	base := &fakeToolCallingModel{streamPipeErr: context.DeadlineExceeded}
	retrying := newRetryModel(base, "key")
	stream, err := retrying.Stream(
		context.Background(), []*schema.Message{schema.UserMessage("hello")},
	)
	require.NoError(t, err)
	for {
		_, err = stream.Recv()
		if err != nil {
			break
		}
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var coded interface{ ErrorCode() string }
	require.ErrorAs(t, err, &coded)
	require.Equal(t, codeModelTimeout, coded.ErrorCode())
}

func TestRetryModelRetriesRateLimitThenSucceeds(t *testing.T) {
	base := &fakeToolCallingModel{generateErrs: []error{
		&goopenai.APIError{HTTPStatusCode: 429, Message: "slow down"},
		&goopenai.APIError{HTTPStatusCode: 429, Message: "slow down"},
	}}
	retrying := newRetryModel(base, "key")
	msg, err := retrying.Generate(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "ok", msg.Content)
	require.Equal(t, 3, base.generateCalls)
}

func TestRetryModelRetriesStreamThenSucceeds(t *testing.T) {
	base := &fakeToolCallingModel{streamErrs: []error{
		&goopenai.RequestError{HTTPStatusCode: 503, Err: errors.New("unavailable")},
	}}
	retrying := newRetryModel(base, "key")
	stream, err := retrying.Stream(context.Background(), nil)
	require.NoError(t, err)
	defer stream.Close()
	require.Equal(t, 2, base.streamCalls)
}

func TestRetryModelDoesNotRetryAuthError(t *testing.T) {
	base := &fakeToolCallingModel{generateErrs: []error{
		&goopenai.APIError{HTTPStatusCode: 401, Message: "bad key"},
	}}
	retrying := newRetryModel(base, "key")
	_, err := retrying.Generate(context.Background(), nil)
	require.Error(t, err)
	require.Equal(t, 1, base.generateCalls)
	var coded *tools.CodedError
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "model_auth", coded.ErrorCode())
}

func TestRetryModelMapsExhaustedServerErrors(t *testing.T) {
	serverErr := &goopenai.APIError{HTTPStatusCode: 500, Message: "boom"}
	base := &fakeToolCallingModel{generateErrs: []error{serverErr, serverErr, serverErr}}
	retrying := newRetryModel(base, "key")
	_, err := retrying.Generate(context.Background(), nil)
	require.Error(t, err)
	require.Equal(t, 3, base.generateCalls)
	var coded *tools.CodedError
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "model_unavailable", coded.ErrorCode())
}

func TestRetryModelReturnsContextErrorUnmapped(t *testing.T) {
	base := &fakeToolCallingModel{generateErrs: []error{context.Canceled}}
	retrying := newRetryModel(base, "key")
	_, err := retrying.Generate(context.Background(), nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, base.generateCalls)
	var coded *tools.CodedError
	require.False(t, errors.As(err, &coded))
}

func TestRetryModelStreamMapsMidStreamError(t *testing.T) {
	base := &fakeToolCallingModel{streamPipeErr: errors.New("broken stream")}
	retrying := newRetryModel(base, "key")
	stream, err := retrying.Stream(context.Background(), nil)
	require.NoError(t, err)
	defer stream.Close()
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Error(t, err)
	var coded *tools.CodedError
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "model_protocol", coded.ErrorCode())
	require.Equal(t, 1, base.streamCalls)
}

func TestMapModelErrorCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"unauthorized", &goopenai.APIError{HTTPStatusCode: 401}, "model_auth"},
		{"forbidden", &goopenai.APIError{HTTPStatusCode: 403}, "model_auth"},
		{"rate limited", &goopenai.APIError{HTTPStatusCode: 429}, "model_rate_limited"},
		{"server", &goopenai.APIError{HTTPStatusCode: 500}, "model_unavailable"},
		{"request 502", &goopenai.RequestError{HTTPStatusCode: 502}, "model_unavailable"},
		{"bad request", &goopenai.APIError{HTTPStatusCode: 400}, "model_protocol"},
		{"network", &url.Error{Op: "Post", URL: "http://x", Err: errors.New("refused")},
			"model_unavailable"},
		{"protocol", errors.New("garbled response"), "model_protocol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := mapModelError(tc.err, "key")
			var coded *tools.CodedError
			require.ErrorAs(t, mapped, &coded)
			require.Equal(t, tc.code, coded.ErrorCode())
			require.ErrorIs(t, mapped, tc.err)
		})
	}
}

func TestMapModelErrorRedactsAPIKey(t *testing.T) {
	err := &goopenai.APIError{HTTPStatusCode: 401, Message: "key sk-secret rejected"}
	mapped := mapModelError(err, "sk-secret")
	require.NotContains(t, mapped.Error(), "sk-secret")
	require.Contains(t, mapped.Error(), "[REDACTED]")
}

type fakeToolCallingModel struct {
	generateCalls  int
	streamCalls    int
	withToolsCalls int
	generateErrs   []error
	streamErrs     []error
	streamPipeErr  error
}

func (f *fakeToolCallingModel) Generate(
	_ context.Context, _ []*schema.Message, _ ...model.Option,
) (*schema.Message, error) {
	f.generateCalls++
	if err := callErr(f.generateErrs, f.generateCalls); err != nil {
		return nil, err
	}
	return schema.AssistantMessage("ok", nil), nil
}

func (f *fakeToolCallingModel) Stream(
	_ context.Context, _ []*schema.Message, _ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	f.streamCalls++
	if err := callErr(f.streamErrs, f.streamCalls); err != nil {
		return nil, err
	}
	if f.streamPipeErr != nil {
		return newErrorPipe(f.streamPipeErr)
	}
	return schema.StreamReaderFromArray(
		[]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

func (f *fakeToolCallingModel) WithTools(
	_ []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	f.withToolsCalls++
	return f, nil
}

func callErr(errs []error, call int) error {
	if call <= len(errs) {
		return errs[call-1]
	}
	return nil
}

// newErrorPipe 先发送一个 chunk，随后抛出流中途错误，
// 以便验证 retry 包装器的 stream 错误映射。
func newErrorPipe(pipeErr error) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](2)
	writer.Send(schema.AssistantMessage("part", nil), nil)
	writer.Send(nil, pipeErr)
	writer.Close()
	return reader, nil
}
