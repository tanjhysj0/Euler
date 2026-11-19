package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

// WorkspaceFinder resolves workspace IDs to absolute directory paths.
type WorkspaceFinder interface {
	WorkspacePath(ctx context.Context, id uuid.UUID) (string, error)
}

// ErrWorkspaceNotFound marks a workspace ID without a database row.
var ErrWorkspaceNotFound = errors.New("workspace not found")

// DBWorkspaceFinder resolves workspaces through the persistence layer. It
// lives outside the HTTP handler so the API layer never touches GORM models
// directly; FileHandler only depends on the WorkspaceFinder interface.
type DBWorkspaceFinder struct{ DB *gorm.DB }

// WorkspacePath loads the workspace directory recorded for id.
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
