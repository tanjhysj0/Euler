package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/modelprofile"
	"euler/internal/persistence"
	"euler/internal/run"
	"euler/internal/tools"
	"euler/internal/workspace"
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
}

// NewDBLoader 组装数据库输入装载器。
func NewDBLoader(
	db *gorm.DB, profiles modelprofile.Resolver, workspaces workspace.WorkspaceFinder,
) *DBLoader {
	return &DBLoader{db: db, profiles: profiles, workspaces: workspaces}
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
