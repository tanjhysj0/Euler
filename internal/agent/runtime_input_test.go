package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/approval"
	"euler/internal/config"
	"euler/internal/event"
	"euler/internal/run"
	"euler/internal/tools"
)

// inputChatModel 用固定的一轮助手回复替代真实模型。
type inputChatModel struct{}

func (*inputChatModel) Stream(
	context.Context, []*schema.Message, ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("完成", nil),
	}), nil
}

func (*inputChatModel) Generate(
	context.Context, []*schema.Message, ...model.Option,
) (*schema.Message, error) {
	return schema.AssistantMessage("完成", nil), nil
}

func (m *inputChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type inputFactory struct{ chatModel model.ToolCallingChatModel }

func (f inputFactory) New(context.Context, ModelSpec) (model.ToolCallingChatModel, error) {
	return f.chatModel, nil
}

// inputMessages 在内存中保存最终助手文本。
type inputMessages struct {
	mu      sync.Mutex
	content string
}

func (m *inputMessages) UpdateContent(
	_ context.Context, _ uuid.UUID, content string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.content = content
	return nil
}

func (m *inputMessages) final() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.content
}

// inputCalls 丢弃工具记账，仅满足端口。
type inputCalls struct{}

func (inputCalls) Start(context.Context, tools.Call) error { return nil }

func (inputCalls) Succeed(context.Context, uuid.UUID, string) error { return nil }

func (inputCalls) Fail(context.Context, uuid.UUID, string, string) error { return nil }

func (inputCalls) Reject(context.Context, uuid.UUID, string) error { return nil }

// inputEvents 丢弃事件，仅满足端口。
type inputEvents struct{}

func (inputEvents) Append(
	_ context.Context, runID uuid.UUID, eventType string, _ any,
) (event.Event, error) {
	return event.Event{RunID: runID, Type: eventType}, nil
}

func (inputEvents) ListAfter(context.Context, uuid.UUID, int64) ([]event.Event, error) {
	return nil, nil
}

func (inputEvents) Subscribe(uuid.UUID) (<-chan event.Event, func()) {
	return nil, func() {}
}

func newInputRuntime(t *testing.T) *Runtime {
	t.Helper()
	return NewRuntime(Deps{
		Events: inputEvents{}, Gate: approval.AutoApprove{},
		Factory: inputFactory{chatModel: &inputChatModel{}},
		Limits: config.AgentLimits{
			MaxModelRounds: 2, MaxToolCalls: 2, MaxOutputBytes: 1024,
		},
		ShellTimeout: time.Second, ShellOutputBytes: 1024, PreviewBytes: 1024,
	})
}

func TestRuntimeRunsFromInputWithoutDatabase(t *testing.T) {
	messages := &inputMessages{}
	runtime := newInputRuntime(t)
	input := RunInput{
		Request: run.Request{
			RunID: uuid.New(), SessionID: uuid.New(), WorkspaceID: uuid.New(),
			AssistantMessageID: uuid.New(),
		},
		History: []*schema.Message{
			schema.SystemMessage("你是测试助手"), schema.UserMessage("打个招呼"),
		},
		Model:    ModelSpec{BaseURL: "http://127.0.0.1:1/v1", Model: "test", APIKey: "sk-test"},
		Dir:      t.TempDir(),
		Calls:    inputCalls{},
		Messages: messages,
	}
	require.NoError(t, runtime.RunInput(context.Background(), input))
	require.Equal(t, "完成", messages.final())
}

func TestRuntimeRequiresLoaderForRun(t *testing.T) {
	runtime := NewRuntime(Deps{Factory: inputFactory{chatModel: &inputChatModel{}}})
	err := runtime.Run(context.Background(), run.Request{})
	require.ErrorIs(t, err, ErrMissingLoader)
}

// 保证 fake 在编译期满足端口。
var (
	_ ChatModelFactory = inputFactory{}
	_ MessageRecorder  = (*inputMessages)(nil)
	_ event.Publisher  = inputEvents{}
)
