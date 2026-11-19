package persistence

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationsDirPrefersWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "migrations"), 0o755))
	t.Chdir(root)
	dir, err := migrationsDir()
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(filepath.Join(root, "migrations"))
	require.NoError(t, err)
	got, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestMigrationsDirMissingReturnsError(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := migrationsDir()
	require.Error(t, err)
}
