package agent

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"euler/internal/approval"
	"euler/internal/config"
	"euler/internal/event"
	"euler/internal/run"
	"euler/internal/tools"
)

// ErrMissingLoader 表示 Runtime 没有配置输入装载器。
var ErrMissingLoader = errors.New("agent: missing input loader")

// ModelSpec 是 agent 内核所需的模型参数，不依赖模型配置服务。
type ModelSpec struct {
	BaseURL string
	Model   string
	APIKey  string
}

// ChatModelFactory 根据模型参数构建基础 chat 模型。
type ChatModelFactory interface {
	New(ctx context.Context, spec ModelSpec) (model.ToolCallingChatModel, error)
}

// MessageRecorder 持久化流式助手消息内容。生产实现写入数据库，
// 测试可以注入内存实现。
type MessageRecorder interface {
	UpdateContent(ctx context.Context, messageID uuid.UUID, content string) error
}

// RunInput 是 Runtime 执行一次 run 所需的全部已装载输入。
// 它把数据库、模型配置和工作区解析留在 Runtime 之外。
type RunInput struct {
	Request  run.Request
	History  []*schema.Message
	Model    ModelSpec
	Dir      string
	Secrets  []string
	Calls    tools.CallRecorder
	Messages MessageRecorder
}

// InputLoader 装载一次 run 的输入。生产实现访问数据库，
// 测试可以直接构造 RunInput。
type InputLoader interface {
	Load(ctx context.Context, request run.Request) (RunInput, error)
}

// Deps 携带单次 run 所需的全部依赖；每次 Run 都会构建全新的工具闭包，
// 避免工作区或审批状态在 run 之间泄漏。
type Deps struct {
	Loader               InputLoader
	Events               event.Publisher
	Gate                 approval.Gate
	Factory              ChatModelFactory
	Limits               config.AgentLimits
	ShellTimeout         time.Duration
	ShellOutputBytes     int
	ShellPathPrefix      string
	ModelTimeout         time.Duration
	ModelReasoningEffort string
	PreviewBytes         int
}

// Runtime 通过 Eino ReAct 循环执行单次 run。
type Runtime struct {
	deps Deps
}

// NewRuntime 构建一个 Runtime；Factory 为 nil 时默认使用真实的 OpenAI
// 兼容 chat 模型工厂。
func NewRuntime(deps Deps) *Runtime {
	if deps.Factory == nil {
		deps.Factory = OpenAIChatModelFactory{
			Timeout:         deps.ModelTimeout,
			ReasoningEffort: deps.ModelReasoningEffort,
		}
	}
	return &Runtime{deps: deps}
}

const (
	messageCompleted = "completed"
	maxStepHeadroom  = 10
	noToolCalls      = 0
)

// Run 装载输入并执行一次 run。
func (rt *Runtime) Run(ctx context.Context, request run.Request) error {
	if rt.deps.Loader == nil {
		return ErrMissingLoader
	}
	input, err := rt.deps.Loader.Load(ctx, request)
	if err != nil {
		return err
	}
	return rt.RunInput(ctx, input)
}

// RunInput 在已装载的输入上驱动模型/工具循环，过程中将助手增量内容
// 流式写入消息记录和事件。
func (rt *Runtime) RunInput(ctx context.Context, input RunInput) error {
	writer := newDeltaWriter(rt.deps.Events, input.Request, input.Messages, input.Model.APIKey)
	reactAgent, err := rt.buildAgent(ctx, input, writer)
	if err != nil {
		return err
	}
	return rt.stream(ctx, reactAgent, input.History, writer)
}

// stream 并行消费最终输出与每轮的 future stream；任一方失败都会通过
// errgroup 的 context 取消另一方。
func (*Runtime) stream(
	ctx context.Context, reactAgent *react.Agent,
	history []*schema.Message, writer *deltaWriter,
) error {
	option, future := react.WithMessageFuture()
	finalStream, err := reactAgent.Stream(ctx, history, option)
	if err != nil {
		return err
	}
	var group errgroup.Group
	group.Go(func() error { return drainFinal(finalStream) })
	group.Go(func() error { return consumeFuture(future, writer) })
	return group.Wait()
}

func (rt *Runtime) buildAgent(
	ctx context.Context, input RunInput, writer *deltaWriter,
) (*react.Agent, error) {
	base, err := rt.deps.Factory.New(ctx, input.Model)
	if err != nil {
		return nil, err
	}
	limited := newRoundLimitedModel(
		newRetryModel(base, input.Model.APIKey), rt.deps.Limits.MaxModelRounds)
	built, err := rt.buildTools(input)
	if err != nil {
		return nil, err
	}
	checker := func(
		ctx context.Context, stream *schema.StreamReader[*schema.Message],
	) (bool, error) {
		return streamHasToolCalls(ctx, stream, writer)
	}
	reactAgent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: limited,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: built, ExecuteSequentially: true,
		},
		MaxStep:               rt.deps.Limits.MaxModelRounds*2 + maxStepHeadroom,
		StreamToolCallChecker: checker,
		// submit 是模型显式收尾的信号：工具执行后直接结束，不再进入下一轮模型调用
		ToolReturnDirectly: map[string]struct{}{tools.SubmitToolName(): {}},
	})
	if err != nil {
		return nil, err
	}
	return reactAgent, nil
}

// streamHasToolCalls 判断整轮模型输出里是否出现工具调用，同时把助手文本
// 增量写入消息记录。Eino 默认只检查首个非空分片，而 DeepSeek 等模型会先
// 输出文字再输出工具调用，默认检查会把这种回复误判为普通文本并丢弃工具调用。
// 因此这里需要读到首个工具调用（或流结束）才能判定；为了让用户仍能实时
// 看到文本，增量内容在检查过程中直接写入 sink，future 路径只负责排空流。
func streamHasToolCalls(
	ctx context.Context, stream *schema.StreamReader[*schema.Message], sink assistantSink,
) (bool, error) {
	defer stream.Close()
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return false, sink.flush(ctx)
		}
		if err != nil {
			return false, err
		}
		found, err := forwardAssistant(ctx, message, sink)
		if err != nil || found {
			return found, err
		}
	}
}

// forwardAssistant 写出一个分片中的助手文本，并报告该分片是否携带工具调用。
func forwardAssistant(
	ctx context.Context, message *schema.Message, sink assistantSink,
) (bool, error) {
	if message.Role == schema.Assistant && message.Content != emptyContent {
		if err := sink.add(ctx, message.Content); err != nil {
			return false, err
		}
	}
	if len(message.ToolCalls) > noToolCalls {
		return true, sink.flush(ctx)
	}
	return false, nil
}

func drainFinal(stream *schema.StreamReader[*schema.Message]) error {
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
