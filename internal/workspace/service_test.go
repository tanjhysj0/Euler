package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNormalizeExistingRejectsFileAndResolvesSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(target, link))
	svc := NewService(root)
	got, err := svc.NormalizeExisting(link)
	require.NoError(t, err)
	resolved, resolveErr := filepath.EvalSymlinks(target)
	require.NoError(t, resolveErr)
	require.Equal(t, resolved, got)
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600))
	_, err = svc.NormalizeExisting(filepath.Join(root, "file"))
	require.Error(t, err)
}

func TestDefaultWorkspaceCleanupOnlyNewEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	svc := NewService(root)
	id := uuid.New()
	path, created, err := svc.CreateDefault(id)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, svc.CleanupCreated(path, created))
	require.NoDirExists(t, path)
}

func TestNormalizeExistingRejectsRelativePath(t *testing.T) {
	svc := NewService(t.TempDir())
	_, err := svc.NormalizeExisting(filepath.Join("relative", "dir"))
	require.Error(t, err)
}

func TestCleanupCreatedKeepsExistingDirectory(t *testing.T) {
	root := t.TempDir()
	svc := NewService(root)
	require.NoError(t, svc.CleanupCreated(root, false))
	require.DirExists(t, root)
}

func TestCleanupCreatedRefusesNonEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	svc := NewService(root)
	path, created, err := svc.CreateDefault(uuid.New())
	require.NoError(t, err)
	require.True(t, created)
	file := filepath.Join(path, "artifact.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	require.Error(t, svc.CleanupCreated(path, true))
	require.DirExists(t, path)
	require.FileExists(t, file)
}

func TestCleanupCreatedSkipsDirectoryWhenNotCreated(t *testing.T) {
	root := t.TempDir()
	svc := NewService(root)
	path, created, err := svc.CreateDefault(uuid.New())
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, svc.CleanupCreated(path, false))
	require.DirExists(t, path)
}

func TestBrowseListsSortedDirectoriesAndParent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "beta"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "alpha"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o600))
	listing, err := Browse(root)
	require.NoError(t, err)
	require.Equal(t, filepath.Base(root), filepath.Base(listing.CurrentPath))
	require.Len(t, listing.Directories, 2)
	require.Equal(t, "alpha", listing.Directories[0].Name)
	expected := filepath.Join(listing.CurrentPath, "alpha")
	require.Equal(t, expected, listing.Directories[0].Path)
	require.NotNil(t, listing.ParentPath)
}

func TestBrowseCapsDirectoriesAtLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxDirectories+5; i++ {
		name := fmt.Sprintf("dir-%03d", i)
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o700))
	}
	listing, err := Browse(root)
	require.NoError(t, err)
	require.Len(t, listing.Directories, maxDirectories)
}

func TestBrowseRejectsMissingDirectory(t *testing.T) {
	_, err := Browse(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}
