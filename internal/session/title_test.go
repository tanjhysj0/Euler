//go:build integration

package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"euler/internal/agent"
	"euler/internal/modelprofile"
	"euler/internal/persistence"
)

const (
	testFirstPrompt = "修复登录接口偶发超时的问题"
	testTitle       = "修复登录超时"
)

// titleFakeProfiles 固定返回一个已解析的模型配置，避免测试依赖密钥环。
type titleFakeProfiles struct{}

func (titleFakeProfiles) Resolve(
	context.Context, uuid.UUID,
) (modelprofile.ResolvedProfile, error) {
	return modelprofile.ResolvedProfile{
		BaseURL: "http://127.0.0.1:1/v1", Model: "test-model", APIKey: "test-key",
	}, nil
}

func (titleFakeProfiles) EnvironmentSecretNames(context.Context) ([]string, error) {
	return nil, nil
}

// titleFakeModel 记录调用次数并返回固定标题。
type titleFakeModel struct {
	mu    sync.Mutex
	calls int
	reply string
	err   error
}

func (m *titleFakeModel) Generate(
	context.Context, []*schema.Message, ...model.Option,
) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return schema.AssistantMessage(m.reply, nil), nil
}

func (*titleFakeModel) Stream(
	context.Context, []*schema.Message, ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not used in title test")
}

func (m *titleFakeModel) WithTools(
	[]*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *titleFakeModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// titleFakeFactory 把固定模型注入 TitleNamer。
type titleFakeFactory struct{ chat model.ToolCallingChatModel }

func (f titleFakeFactory) New(
	context.Context, agent.ModelSpec,
) (model.ToolCallingChatModel, error) {
	return f.chat, nil
}

func TestNameSessionGeneratesTitleOnFirstRound(t *testing.T) {
	svc, db, _ := newSessionServiceWithRoot(t)
	created, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)
	insertUserMessage(t, db, persistence.Message{
		SessionID: created.ID, SessionSeq: 1, Content: testFirstPrompt,
	})
	chat := &titleFakeModel{reply: `"` + testTitle + `"`}
	namer := NewTitleNamer(db, titleFakeProfiles{}, titleFakeFactory{chat: chat})
	require.NoError(t, namer.NameSession(context.Background(), created.ID))
	require.Equal(t, 1, chat.callCount())
	require.Equal(t, testTitle, loadTitle(t, db, created.ID))
}

func TestNameSessionSkipsWhenTitleAlreadySet(t *testing.T) {
	svc, db, _ := newSessionServiceWithRoot(t)
	created, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)
	insertUserMessage(t, db, persistence.Message{
		SessionID: created.ID, SessionSeq: 1, Content: testFirstPrompt,
	})
	require.NoError(t, db.Model(&persistence.Session{}).
		Where(idFilter, created.ID).Update("title", "已有标题").Error)
	chat := &titleFakeModel{reply: testTitle}
	namer := NewTitleNamer(db, titleFakeProfiles{}, titleFakeFactory{chat: chat})
	require.NoError(t, namer.NameSession(context.Background(), created.ID))
	require.Equal(t, 0, chat.callCount())
	require.Equal(t, "已有标题", loadTitle(t, db, created.ID))
}

func TestNameSessionKeepsTitleOnModelFailure(t *testing.T) {
	svc, db, _ := newSessionServiceWithRoot(t)
	created, err := svc.Create(context.Background(), CreateInput{ProfileID: testProfileID})
	require.NoError(t, err)
	insertUserMessage(t, db, persistence.Message{
		SessionID: created.ID, SessionSeq: 1, Content: testFirstPrompt,
	})
	chat := &titleFakeModel{err: errors.New("upstream unavailable: test-key")}
	namer := NewTitleNamer(db, titleFakeProfiles{}, titleFakeFactory{chat: chat})
	err = namer.NameSession(context.Background(), created.ID)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "test-key")
	require.Equal(t, defaultSessionTitle, loadTitle(t, db, created.ID))
}

func TestCleanTitle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "去掉引号与空白", raw: "  “修复登录超时”  ", want: "修复登录超时"},
		{name: "去掉标题前缀", raw: "标题：修复登录超时", want: "修复登录超时"},
		{name: "只保留首行", raw: "修复登录超时\n解释：因为接口阻塞", want: "修复登录超时"},
		{name: "空输出保持为空", raw: "   ", want: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, cleanTitle(testCase.raw))
		})
	}
	require.Len(t, []rune(cleanTitle(strings.Repeat("长", 100))), titleMaxLength)
}

func insertUserMessage(t *testing.T, db *gorm.DB, message persistence.Message) {
	t.Helper()
	message.ID = uuid.New()
	message.Role = titleRoleUser
	message.Status = "completed"
	message.CreatedAt = time.Now().UTC()
	require.NoError(t, db.Create(&message).Error)
}

func loadTitle(t *testing.T, db *gorm.DB, sessionID uuid.UUID) string {
	t.Helper()
	var session persistence.Session
	require.NoError(t, db.First(&session, idFilter, sessionID).Error)
	return session.Title
}
