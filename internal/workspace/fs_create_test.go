package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateDirectoryCreatesSingleLevel(t *testing.T) {
	parent := t.TempDir()
	resolvedParent, err := filepath.EvalSymlinks(parent)
	require.NoError(t, err)
	directory, err := CreateDirectory(parent, "project")
	require.NoError(t, err)
	require.Equal(t, "project", directory.Name)
	require.Equal(t, filepath.Join(resolvedParent, "project"), directory.Path)
	require.DirExists(t, directory.Path)
}

func TestCreateDirectoryRejectsInvalidName(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"", ".", "..", "a/b", "../escape", "a/../b"} {
		_, err := CreateDirectory(parent, name)
		require.ErrorIs(t, err, ErrInvalidPath, "名称 %q 必须被拒绝", name)
	}
}

func TestCreateDirectoryRejectsMissingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing")
	_, err := CreateDirectory(parent, "project")
	require.ErrorIs(t, err, ErrInvalidPath)
}

func TestCreateDirectoryRejectsExistingDirectory(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(parent, "project"), 0o700))
	_, err := CreateDirectory(parent, "project")
	require.ErrorIs(t, err, os.ErrExist)
}
