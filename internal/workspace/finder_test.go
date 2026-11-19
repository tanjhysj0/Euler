//go:build integration

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"euler/internal/persistence"
)

const testDatabaseURLEnv = "TEST_DATABASE_URL"

// newFinderTestDB 在一个事务中打开测试数据库，该事务会在清理阶段
// 回滚，因此测试数据绝不会残留到测试之外。
func newFinderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		t.Fatalf("%s is required for integration tests", testDatabaseURLEnv)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	t.Chdir(repoRoot)
	require.NoError(t, persistence.Migrate(databaseURL))
	db, err := persistence.Open(context.Background(), databaseURL)
	require.NoError(t, err)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback().Error)
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		require.NoError(t, sqlDB.Close())
	})
	return tx
}

func TestDBWorkspaceFinderResolvesRecordedPath(t *testing.T) {
	finder := DBWorkspaceFinder{DB: newFinderTestDB(t)}
	row := persistence.Workspace{ID: uuid.New(), Path: t.TempDir()}
	require.NoError(t, finder.DB.Create(&row).Error)
	got, err := finder.WorkspacePath(context.Background(), row.ID)
	require.NoError(t, err)
	require.Equal(t, row.Path, got)
}

func TestDBWorkspaceFinderMapsMissingRow(t *testing.T) {
	finder := DBWorkspaceFinder{DB: newFinderTestDB(t)}
	_, err := finder.WorkspacePath(context.Background(), uuid.New())
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
}
