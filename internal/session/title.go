package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/agent"
	"euler/internal/logging"
	"euler/internal/modelprofile"
	"euler/internal/persistence"
)

const (
	// titleRoleUser 是标题生成判定首轮消息时使用的角色值。
	titleRoleUser = "user"
	// noMessages 表示会话还没有任何用户消息。
	noMessages = 0
	// firstMessage 是用户消息列表的首个下标。
	firstMessage = 0
	// minIndex 是非负下标的起始值。
	minIndex = 0
	// titlePrompt 约束模型只输出一个简短标题，避免把解释带入会话列表。
	titlePrompt = `你是会话标题生成器。阅读用户的第一条需求，输出一个简洁的中文标题。
要求：
1. 概括任务目标，不超过 20 个汉字；
2. 只输出标题本身，不要引号、句号、序号或任何解释。`
	// titleInputLimit 是送入模型的首条消息最大字符数，避免超长需求浪费 token。
	titleInputLimit = 1000
	// titleMaxLength 是落库标题的最大字符数，防止模型输出过长文本。
	titleMaxLength = 40
	// titleTimeout 限制单次标题生成的耗时，避免拖慢 run 的完成事件。
	titleTimeout = 10 * time.Second
)

// titlePrefixes 是模型可能附带输出的前缀，落库前需要剔除。
var titlePrefixes = []string{"会话标题：", "会话标题:", "标题：", "标题:"}

// TitleNamer 依据会话首条用户需求生成可读标题，替换默认的占位标题。
// 只有首轮 run 才会触发生成，后续轮次直接跳过。
type TitleNamer struct {
	db       *gorm.DB
	profiles modelprofile.Resolver
	factory  agent.ChatModelFactory
}

// NewTitleNamer 组装标题生成器；factory 为空时上层不应启用标题生成。
func NewTitleNamer(
	db *gorm.DB, profiles modelprofile.Resolver, factory agent.ChatModelFactory,
) *TitleNamer {
	return &TitleNamer{db: db, profiles: profiles, factory: factory}
}

// NameSession 生成并写入会话标题。标题已不是占位值、没有用户消息或标题
// 未变化时直接返回 nil，因此同一次会话只会调用一次模型。
func (n *TitleNamer) NameSession(ctx context.Context, sessionID uuid.UUID) error {
	var session persistence.Session
	err := n.db.WithContext(ctx).
		Select("id", "title", "model_profile_id").
		First(&session, idFilter, sessionID).Error
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if session.Title != defaultSessionTitle {
		return nil
	}
	first, ok, err := n.firstUserMessage(ctx, sessionID)
	if err != nil || !ok {
		return err
	}
	title, err := n.generate(ctx, session, first)
	if err != nil || title == emptyString || title == session.Title {
		return err
	}
	err = n.db.WithContext(ctx).Model(&persistence.Session{}).
		Where(idFilter, sessionID).Update("title", title).Error
	if err != nil {
		return fmt.Errorf("update session title: %w", err)
	}
	return nil
}

// firstUserMessage 读取会话首条用户消息；没有消息时返回 ok=false，
// 让调用方跳过标题生成。
func (n *TitleNamer) firstUserMessage(
	ctx context.Context, sessionID uuid.UUID,
) (string, bool, error) {
	var messages []persistence.Message
	err := n.db.WithContext(ctx).
		Select("content").
		Where("session_id = ? AND role = ?", sessionID, titleRoleUser).
		Order("session_seq ASC").
		Limit(1).
		Find(&messages).Error
	if err != nil {
		return emptyString, false, fmt.Errorf("load first user message: %w", err)
	}
	if len(messages) == noMessages ||
		strings.TrimSpace(messages[firstMessage].Content) == emptyString {
		return emptyString, false, nil
	}
	return messages[firstMessage].Content, true, nil
}

// generate 调用会话绑定的模型生成标题，失败信息中的密钥会被脱敏。
func (n *TitleNamer) generate(
	ctx context.Context, session persistence.Session, first string,
) (string, error) {
	profile, err := n.profiles.Resolve(ctx, session.ModelProfileID)
	if err != nil {
		return emptyString, fmt.Errorf("resolve model profile: %w", err)
	}
	chatModel, err := n.factory.New(ctx, agent.ModelSpec{
		BaseURL: profile.BaseURL, Model: profile.Model, APIKey: profile.APIKey,
	})
	if err != nil {
		return emptyString, fmt.Errorf("create chat model: %w", err)
	}
	genCtx, cancel := context.WithTimeout(ctx, titleTimeout)
	defer cancel()
	message, err := chatModel.Generate(genCtx, []*schema.Message{
		schema.SystemMessage(titlePrompt),
		schema.UserMessage(truncateRunes(first, titleInputLimit)),
	})
	if err != nil {
		return emptyString, fmt.Errorf(
			"generate session title: %s", logging.Redact(err.Error(), profile.APIKey),
		)
	}
	return cleanTitle(message.Content), nil
}

// cleanTitle 去掉模型输出中的换行、前缀与引号，并限制最大长度。
func cleanTitle(raw string) string {
	title := strings.TrimSpace(raw)
	if index := strings.IndexByte(title, '\n'); index >= minIndex {
		title = title[:index]
	}
	title = strings.TrimSpace(title)
	for _, prefix := range titlePrefixes {
		title = strings.TrimPrefix(title, prefix)
	}
	title = strings.Trim(strings.TrimSpace(title), "\"'“”‘’《》")
	return truncateRunes(title, titleMaxLength)
}

// truncateRunes 按字符截断，避免把多字节中文截成乱码。
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit]))
}
