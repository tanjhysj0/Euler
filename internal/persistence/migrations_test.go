package persistence

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"euler/migrations"
)

func TestEmbeddedMigrationsAvailable(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		entries, err := fs.ReadDir(migrations.FS(), dialect)
		require.NoError(t, err, "dialect=%s", dialect)
		require.NotEmpty(t, entries, "dialect=%s", dialect)
	}
	content, err := fs.ReadFile(migrations.FS(), "sqlite/000001_init.up.sql")
	require.NoError(t, err)
	require.Contains(t, string(content), "CREATE TABLE")
}

// TestMigrateUsesEmbeddedScriptsOutsideRepo 模拟安装后的运行环境：
// 工作目录没有 migrations/ 目录时，迁移仍应依赖内嵌脚本完成。
func TestMigrateUsesEmbeddedScriptsOutsideRepo(t *testing.T) {
	t.Chdir(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "euler.db")
	require.NoError(t, Migrate("sqlite", dbPath))
	// 重复执行应幂等，不会重复建表或报错。
	require.NoError(t, Migrate("sqlite", dbPath))
}
