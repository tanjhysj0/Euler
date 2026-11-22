//go:build integration

package agent

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/google/uuid"

	"euler/internal/persistence"
)

// fakeFactory 返回预设的 chat 模型，供集成测试跳过网络调用。
type fakeFactory struct{ chatModel model.ToolCallingChatModel }

func (f fakeFactory) New(
	context.Context, ModelSpec,
) (model.ToolCallingChatModel, error) {
	return f.chatModel, nil
}

// runtimeNoopMessages 丢弃流式内容，供只关心错误映射的测试使用。
type runtimeNoopMessages struct{}

func (runtimeNoopMessages) UpdateMessage(
	context.Context, uuid.UUID, string, []persistence.Part,
) error {
	return nil
}
