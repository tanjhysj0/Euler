package tools

import (
	"context"

	"github.com/google/uuid"

	"euler/internal/workspace"
)

// Call 描述一次工具调用的持久化输入。
type Call struct {
	ID            uuid.UUID
	RunID         uuid.UUID
	Name          string
	ArgumentsJSON []byte
}

// CallRecorder 持久化工具调用的生命周期。生产实现基于数据库，
// 测试可以注入纯内存实现，从而不依赖 PostgreSQL。
type CallRecorder interface {
	// Start 写入一条进行中的工具调用。
	Start(ctx context.Context, call Call) error
	// Succeed 记录成功结果。
	Succeed(ctx context.Context, id uuid.UUID, resultJSON string) error
	// Fail 记录执行失败；resultJSON 可为空，message 是脱敏后的错误消息。
	Fail(ctx context.Context, id uuid.UUID, resultJSON, message string) error
	// Reject 记录被审批拒绝的调用；状态已由审批服务写入。
	Reject(ctx context.Context, id uuid.UUID, resultJSON string) error
}

// FileStore 是文件工具依赖的工作区端口。生产实现是 workspace.FileStore，
// 测试可以用内存实现替代。
type FileStore interface {
	Read(ctx context.Context, path string) ([]byte, error)
	List(ctx context.Context) ([]workspace.FileNode, error)
	Search(ctx context.Context, query string) ([]string, error)
	PrepareWrite(ctx context.Context, path string, content []byte) (workspace.FileChange, error)
	PreparePatch(
		ctx context.Context, path, oldString, newString string,
	) (workspace.FileChange, error)
	Delete(ctx context.Context, path string) (workspace.FileChange, error)
}

// Executor 在工作区目录内执行 shell 命令。生产实现是 Shell，
// 测试可以注入固定结果；未来容器化时只需替换此端口。
type Executor interface {
	Execute(ctx context.Context, request ShellRequest) ShellResult
}
