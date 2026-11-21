package agent

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/modelprofile"
	"euler/internal/persistence"
	"euler/internal/run"
	"euler/internal/tools"
	"euler/internal/workspace"
)

// promptFS 保存各类 Agent 的 system prompt 模板；模板放在固定目录中，
// 通过配置名选择，便于后续增加多类 Agent。
//
//go:embed prompts/*.md
var promptFS embed.FS

const (
	defaultPromptTemplate = "coding"
	promptPlaceholder     = "{{workspace}}"
)

const (
	roleUser      = "user"
	roleAssistant = "assistant"
	emptyHistory  = 0
)

// DBLoader 从数据库装载一次 run 的输入。
type DBLoader struct {
	db         *gorm.DB
	profiles   modelprofile.Resolver
	workspaces workspace.WorkspaceFinder
	promptName string
}

// NewDBLoader 组装数据库输入装载器。promptName 为空时使用默认模板。
func NewDBLoader(
	db *gorm.DB,
	profiles modelprofile.Resolver,
	workspaces workspace.WorkspaceFinder,
	promptName string,
) *DBLoader {
	return &DBLoader{
		db: db, profiles: profiles, workspaces: workspaces, promptName: promptName,
	}
}

// Load 读取会话、模型配置、工作区与消息历史，并绑定本次 run 的记账端口。
func (l *DBLoader) Load(ctx context.Context, request run.Request) (RunInput, error) {
	var session persistence.Session
	if err := l.db.WithContext(ctx).
		First(&session, "id = ?", request.SessionID).Error; err != nil {
		return RunInput{}, fmt.Errorf("load session: %w", err)
	}
	profile, err := l.profiles.Resolve(ctx, session.ModelProfileID)
	if err != nil {
		return RunInput{}, fmt.Errorf("resolve model profile: %w", err)
	}
	dir, err := l.workspaces.WorkspacePath(ctx, request.WorkspaceID)
	if err != nil {
		return RunInput{}, fmt.Errorf("resolve workspace: %w", err)
	}
	history, err := l.loadHistory(ctx, request, dir)
	if err != nil {
		return RunInput{}, err
	}
	secrets, err := l.profiles.EnvironmentSecretNames(ctx)
	if err != nil {
		return RunInput{}, fmt.Errorf("list environment secrets: %w", err)
	}
	return RunInput{
		Request: request,
		History: history,
		Model: ModelSpec{
			BaseURL: profile.BaseURL, Model: profile.Model, APIKey: profile.APIKey,
		},
		Dir:      dir,
		Secrets:  secrets,
		Calls:    tools.NewGormRecorder(l.db),
		Messages: gormMessageRecorder{db: l.db},
	}, nil
}

// loadHistory 构建展示用历史：一条 system 消息、此前已完成的
// user/assistant 消息，最后是当前的 user 消息。工具交互只保留在
// Eino 状态内部，不会进入持久化历史。
func (l *DBLoader) loadHistory(
	ctx context.Context, request run.Request, dir string,
) ([]*schema.Message, error) {
	var current persistence.Message
	err := l.db.WithContext(ctx).
		Where("run_id = ? AND role = ?", request.RunID, roleUser).
		First(&current).Error
	if err != nil {
		return nil, fmt.Errorf("load user message: %w", err)
	}
	var past []persistence.Message
	err = l.db.WithContext(ctx).
		Where("session_id = ? AND session_seq < ? AND role IN ? AND status = ?",
			request.SessionID, current.SessionSeq,
			[]string{roleUser, roleAssistant}, messageCompleted).
		Order("session_seq ASC").Find(&past).Error
	if err != nil {
		return nil, fmt.Errorf("load history: %w", err)
	}
	history := make([]*schema.Message, emptyHistory, len(past)+2)
	prompt, err := renderSystemPrompt(l.promptName, dir)
	if err != nil {
		return nil, err
	}
	history = append(history, schema.SystemMessage(prompt))
	for _, message := range past {
		if display := displayMessage(message); display != nil {
			history = append(history, display)
		}
	}
	return append(history, schema.UserMessage(current.Content)), nil
}

// renderSystemPrompt 按名称加载模板并注入工作区路径。
func renderSystemPrompt(name, dir string) (string, error) {
	if name == "" {
		name = defaultPromptTemplate
	}
	data, err := promptFS.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("load prompt template %q: %w", name, err)
	}
	return strings.ReplaceAll(string(data), promptPlaceholder, dir), nil
}

// displayMessage 把历史消息转换为模型可接受的格式。正文为空的助手消息
// （模型只返回思考内容、Run 被取消或失败）必须跳过：上游会拒绝
// content、tool_calls、reasoning_content 同时为空的 assistant 消息。
func displayMessage(message persistence.Message) *schema.Message {
	if message.Role == roleAssistant {
		if strings.TrimSpace(message.Content) == emptyContent {
			return nil
		}
		return schema.AssistantMessage(message.Content, nil)
	}
	return schema.UserMessage(message.Content)
}

// gormMessageRecorder 把流式助手消息内容写回数据库。
type gormMessageRecorder struct {
	db *gorm.DB
}

// UpdateContent 更新指定消息的内容。
func (r gormMessageRecorder) UpdateContent(
	ctx context.Context, messageID uuid.UUID, content string,
) error {
	err := r.db.WithContext(ctx).Model(&persistence.Message{}).
		Where("id = ?", messageID).Update("content", content).Error
	if err != nil {
		return fmt.Errorf("update message content: %w", err)
	}
	return nil
}
