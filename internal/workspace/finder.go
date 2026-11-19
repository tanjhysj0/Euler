package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

// WorkspaceFinder 将工作区 ID 解析为绝对目录路径。
type WorkspaceFinder interface {
	WorkspacePath(ctx context.Context, id uuid.UUID) (string, error)
}

// ErrWorkspaceNotFound 表示工作区 ID 在数据库中没有对应记录。
var ErrWorkspaceNotFound = errors.New("workspace not found")

// DBWorkspaceFinder 通过持久化层解析工作区。它独立于 HTTP handler
// 存在，使 API 层绝不直接接触 GORM 模型；FileHandler 只依赖
// WorkspaceFinder 接口。
type DBWorkspaceFinder struct{ DB *gorm.DB }

// WorkspacePath 加载为 id 记录的工作区目录。
func (f DBWorkspaceFinder) WorkspacePath(ctx context.Context, id uuid.UUID) (string, error) {
	var row persistence.Workspace
	err := f.DB.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrWorkspaceNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load workspace: %w", err)
	}
	return row.Path, nil
}
