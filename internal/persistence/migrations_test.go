package persistence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationsDirPrefersWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "migrations", "sqlite"), 0o755))
	t.Chdir(root)
	dir, err := migrationsDir("migrations/sqlite")
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(filepath.Join(root, "migrations", "sqlite"))
	require.NoError(t, err)
	got, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestMigrationsDirMissingReturnsError(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := migrationsDir("migrations/nonexistent")
	require.Error(t, err)
}
