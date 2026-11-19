package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"euler/internal/approval"
	"euler/internal/config"
	"euler/internal/event"
	"euler/internal/modelprofile"
	"euler/internal/persistence"
	"euler/internal/run"
	"euler/internal/workspace"
)

const emptyHistory = 0

// ChatModelFactory builds the base chat model for one resolved profile.
type ChatModelFactory interface {
	New(ctx context.Context, profile modelprofile.ResolvedProfile) (
		model.ToolCallingChatModel, error)
}

// Deps carries everything one run needs; each Run builds fresh tool closures
// so no workspace or approval state leaks between runs.
type Deps struct {
	DB               *gorm.DB
	Events           event.Publisher
	Gate             approval.Gate
	Profiles         modelprofile.Resolver
	Workspaces       workspace.WorkspaceFinder
	Factory          ChatModelFactory
	Limits           config.AgentLimits
	ShellTimeout     time.Duration
	ShellOutputBytes int
	PreviewBytes     int
}

// Runtime executes one run through the Eino ReAct loop.
type Runtime struct {
	deps Deps
}

// NewRuntime builds a Runtime; a nil Factory defaults to the real OpenAI
// compatible chat model factory.
func NewRuntime(deps Deps) *Runtime {
	if deps.Factory == nil {
		deps.Factory = OpenAIChatModelFactory{}
	}
	return &Runtime{deps: deps}
}

const (
	roleUser         = "user"
	roleAssistant    = "assistant"
	messageCompleted = "completed"
	maxStepHeadroom  = 10
)

// Run drives the model/tool loop until the final answer, streaming assistant
// deltas into the run's message and events along the way.
func (rt *Runtime) Run(ctx context.Context, request run.Request) error {
	var session persistence.Session
	if err := rt.deps.DB.WithContext(ctx).
		First(&session, "id = ?", request.SessionID).Error; err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	profile, err := rt.deps.Profiles.Resolve(ctx, session.ModelProfileID)
	if err != nil {
		return fmt.Errorf("resolve model profile: %w", err)
	}
	dir, err := rt.deps.Workspaces.WorkspacePath(ctx, request.WorkspaceID)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	history, err := rt.loadHistory(ctx, request, dir)
	if err != nil {
		return err
	}
	reactAgent, err := rt.buildAgent(ctx, request, profile, dir)
	if err != nil {
		return err
	}
	writer := newDeltaWriter(rt.deps, request, profile.APIKey)
	return rt.stream(ctx, reactAgent, history, writer)
}

// stream drains the final output in parallel with the per-round future
// streams; either failure cancels the other through the errgroup context.
func (*Runtime) stream(
	ctx context.Context, reactAgent *react.Agent,
	history []*schema.Message, writer *deltaWriter,
) error {
	option, future := react.WithMessageFuture()
	finalStream, err := reactAgent.Stream(ctx, history, option)
	if err != nil {
		return err
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return drainFinal(finalStream) })
	group.Go(func() error { return consumeFuture(groupCtx, future, writer) })
	return group.Wait()
}

func (rt *Runtime) buildAgent(
	ctx context.Context, request run.Request,
	profile modelprofile.ResolvedProfile, dir string,
) (*react.Agent, error) {
	base, err := rt.deps.Factory.New(ctx, profile)
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}
	limited := newRoundLimitedModel(
		newRetryModel(base, profile.APIKey), rt.deps.Limits.MaxModelRounds)
	built, err := rt.buildTools(ctx, request, profile, dir)
	if err != nil {
		return nil, err
	}
	reactAgent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: limited,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: built, ExecuteSequentially: true,
		},
		MaxStep: rt.deps.Limits.MaxModelRounds*2 + maxStepHeadroom,
	})
	if err != nil {
		return nil, fmt.Errorf("create react agent: %w", err)
	}
	return reactAgent, nil
}

// loadHistory builds the display history: one system message, prior completed
// user/assistant messages, then the current user message. Tool traffic stays
// inside Eino state and never enters persisted history.
func (rt *Runtime) loadHistory(
	ctx context.Context, request run.Request, dir string,
) ([]*schema.Message, error) {
	var current persistence.Message
	err := rt.deps.DB.WithContext(ctx).
		Where("run_id = ? AND role = ?", request.RunID, roleUser).
		First(&current).Error
	if err != nil {
		return nil, fmt.Errorf("load user message: %w", err)
	}
	var past []persistence.Message
	err = rt.deps.DB.WithContext(ctx).
		Where("session_id = ? AND session_seq < ? AND role IN ? AND status = ?",
			request.SessionID, current.SessionSeq,
			[]string{roleUser, roleAssistant}, messageCompleted).
		Order("session_seq ASC").Find(&past).Error
	if err != nil {
		return nil, fmt.Errorf("load history: %w", err)
	}
	history := make([]*schema.Message, emptyHistory, len(past)+2)
	history = append(history, schema.SystemMessage(systemPrompt(dir)))
	for _, message := range past {
		history = append(history, displayMessage(message))
	}
	return append(history, schema.UserMessage(current.Content)), nil
}

func systemPrompt(dir string) string {
	return "你是 Euler 本地编码助手。工作区绝对路径：" + dir + "。" +
		"文件工具只能访问该工作区内的路径。写入、补丁、删除文件和执行 shell 命令都需要用户审批。" +
		"回答保持简洁。"
}

func displayMessage(message persistence.Message) *schema.Message {
	if message.Role == roleAssistant {
		return schema.AssistantMessage(message.Content, nil)
	}
	return schema.UserMessage(message.Content)
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
