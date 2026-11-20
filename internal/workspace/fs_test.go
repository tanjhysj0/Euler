package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileStoreRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	store := NewFileStore(root, 64*1024)
	_, err := store.Read(context.Background(), "../secret.txt")
	require.ErrorIs(t, err, ErrOutsideWorkspace)
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link.txt")))
	_, err = store.Read(context.Background(), "link.txt")
	require.ErrorIs(t, err, ErrOutsideWorkspace)
	_, err = store.Read(context.Background(), "/etc/passwd")
	require.ErrorIs(t, err, ErrOutsideWorkspace)
}

func TestFileStoreReadRejectsEscapeVariants(t *testing.T) {
	store := NewFileStore(t.TempDir(), 64*1024)
	paths := []string{
		"/link.txt", "..", "a/../../b.txt", "nested/../../../escape.txt",
	}
	for _, requested := range paths {
		_, err := store.Read(context.Background(), requested)
		require.ErrorIs(t, err, ErrOutsideWorkspace, "路径 %s 必须被拒绝", requested)
	}
}

func TestFileStoreReadMissingAndDirectory(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root, 64*1024)
	_, err := store.Read(context.Background(), "missing/parent/file.txt")
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, os.Mkdir(filepath.Join(root, "dir"), 0o700))
	_, err = store.Read(context.Background(), "dir")
	require.ErrorIs(t, err, ErrNotRegularFile)
}

func TestFileStoreReadTooLargeReturnsExplicitError(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("a", 21)
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), []byte(content), 0o600))
	store := NewFileStore(root, 20)
	_, err := store.Read(context.Background(), "big.txt")
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestFileStoreReadUTF8RoundTrip(t *testing.T) {
	root := t.TempDir()
	content := "中文内容 with UTF-8 ✓"
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.md"), []byte(content), 0o600))
	store := NewFileStore(root, 64*1024)
	data, err := store.Read(context.Background(), "notes.md")
	require.NoError(t, err)
	require.Equal(t, content, string(data))
}

func TestFileStoreListNestedWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "alpha"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "alpha", "beta"), 0o700))
	writeTestFile(t, filepath.Join(root, "alpha", "one.txt"), "one")
	writeTestFile(t, filepath.Join(root, "alpha", "beta", "two.txt"), "two")
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "leaked.txt"), "leaked")
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "out-link")))

	store := NewFileStore(root, 64*1024)
	nodes, err := store.List(context.Background())
	require.NoError(t, err)

	paths := make([]string, 0, len(nodes))
	byPath := make(map[string]FileNode, len(nodes))
	for _, node := range nodes {
		paths = append(paths, node.Path)
		byPath[node.Path] = node
	}
	require.Equal(t, []string{
		"alpha", "alpha/beta", "alpha/beta/two.txt", "alpha/one.txt", "out-link",
	}, paths)
	require.Equal(t, typeDir, byPath["alpha"].Type)
	require.Equal(t, typeFile, byPath["alpha/one.txt"].Type)
	require.Equal(t, int64(len("one")), byPath["alpha/one.txt"].Size)
	require.False(t, byPath["alpha/one.txt"].ModTime.IsZero())
	// 符号链接本身列出，但不遍历其目标内容。
	require.Equal(t, typeFile, byPath["out-link"].Type)
	require.NotContains(t, paths, "out-link/leaked.txt")
}

func TestFileStoreListCapsNodes(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxListNodes+5; i++ {
		writeTestFile(t, filepath.Join(root, strings.ToLower(padNumber(i))+".txt"), "x")
	}
	store := NewFileStore(root, 64*1024)
	nodes, err := store.List(context.Background())
	require.NoError(t, err)
	require.Len(t, nodes, maxListNodes)
}

func TestFileStoreListDirShallow(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "alpha"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "alpha", "beta"), 0o700))
	writeTestFile(t, filepath.Join(root, "alpha", "one.txt"), "one")
	writeTestFile(t, filepath.Join(root, "alpha", "beta", "two.txt"), "two")
	writeTestFile(t, filepath.Join(root, "top.txt"), "hi")
	store := NewFileStore(root, 64*1024)

	nodes, err := store.ListDir(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "top.txt"}, nodePaths(nodes))
	require.Equal(t, typeDir, nodes[0].Type)
	require.Equal(t, typeFile, nodes[1].Type)
	require.Equal(t, int64(len("hi")), nodes[1].Size)

	inner, err := store.ListDir(context.Background(), "alpha")
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/beta", "alpha/one.txt"}, nodePaths(inner))
}

func TestFileStoreListDirRejectsEscapeAndFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "x")
	store := NewFileStore(root, 64*1024)

	_, err := store.ListDir(context.Background(), "../outside")
	require.ErrorIs(t, err, ErrOutsideWorkspace)

	_, err = store.ListDir(context.Background(), "a.txt")
	require.ErrorIs(t, err, ErrNotDirectory)

	_, err = store.ListDir(context.Background(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFileStoreListDirCapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxListNodes+5; i++ {
		writeTestFile(t, filepath.Join(root, strings.ToLower(padNumber(i))+".txt"), "x")
	}
	store := NewFileStore(root, 64*1024)
	nodes, err := store.ListDir(context.Background(), ".")
	require.NoError(t, err)
	require.Len(t, nodes, maxListNodes)
}

func nodePaths(nodes []FileNode) []string {
	paths := make([]string, emptyCapacity, len(nodes))
	for _, node := range nodes {
		paths = append(paths, node.Path)
	}
	return paths
}

func TestFileStoreSearchSortedAndCapped(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "notes"), 0o700))
	writeTestFile(t, filepath.Join(root, "report-a.txt"), "a")
	writeTestFile(t, filepath.Join(root, "report-b.txt"), "b")
	writeTestFile(t, filepath.Join(root, "notes", "report-c.txt"), "c")
	writeTestFile(t, filepath.Join(root, "other.txt"), "d")
	store := NewFileStore(root, 64*1024)
	matches, err := store.Search(context.Background(), "report")
	require.NoError(t, err)
	require.Equal(t, []string{
		"notes/report-c.txt", "report-a.txt", "report-b.txt",
	}, matches)

	for i := 0; i < maxSearchResults+5; i++ {
		writeTestFile(t, filepath.Join(root, "hit-"+padNumber(i)+".txt"), "x")
	}
	capped, err := store.Search(context.Background(), "hit-")
	require.NoError(t, err)
	require.Len(t, capped, maxSearchResults)
	for i, match := range capped {
		require.Equal(t, "hit-"+padNumber(i)+".txt", match)
	}
}

func TestFileStorePrepareWriteNewFile(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root, 64*1024)
	change, err := store.PrepareWrite(context.Background(), "nested/report.md", []byte("hello\n"))
	require.NoError(t, err)
	require.Contains(t, change.Diff, "+hello")
	require.Contains(t, change.Diff, "/dev/null")
	// Prepare 只计算变更，Apply 前不产生任何写盘副作用。
	require.NoDirExists(t, filepath.Join(root, "nested"))
	require.NoError(t, change.Apply(context.Background()))
	data, err := os.ReadFile(filepath.Join(root, "nested", "report.md"))
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(data))
	info, err := os.Stat(filepath.Join(root, "nested", "report.md"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestFileStorePrepareWriteOverwrite(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\n")
	store := NewFileStore(root, 64*1024)
	change, err := store.PrepareWrite(context.Background(), "notes.txt", []byte("one\nTWO\n"))
	require.NoError(t, err)
	require.Contains(t, change.Diff, "-two")
	require.Contains(t, change.Diff, "+TWO")
	data, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, "one\ntwo\n", string(data))
	require.NoError(t, change.Apply(context.Background()))
	data, err = os.ReadFile(filepath.Join(root, "notes.txt"))
	require.NoError(t, err)
	require.Equal(t, "one\nTWO\n", string(data))
}

func TestFileStorePrepareWriteApplyRejectsSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target.txt")
	writeTestFile(t, outside, "secret")
	writeTestFile(t, filepath.Join(root, "file.txt"), "safe")
	store := NewFileStore(root, 64*1024)
	change, err := store.PrepareWrite(context.Background(), "file.txt", []byte("new"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "file.txt")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "file.txt")))
	require.ErrorIs(t, change.Apply(context.Background()), ErrOutsideWorkspace)
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "secret", string(data))
}

func TestFileStorePreparePatchReplacesExactlyOnce(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "doc.txt"), "alpha\nbeta\nalpha\n")
	store := NewFileStore(root, 64*1024)
	change, err := store.PreparePatch(context.Background(), "doc.txt", "beta", "gamma")
	require.NoError(t, err)
	require.Contains(t, change.Diff, "-beta")
	require.Contains(t, change.Diff, "+gamma")
	data, err := os.ReadFile(filepath.Join(root, "doc.txt"))
	require.NoError(t, err)
	require.Equal(t, "alpha\nbeta\nalpha\n", string(data))
	require.NoError(t, change.Apply(context.Background()))
	data, err = os.ReadFile(filepath.Join(root, "doc.txt"))
	require.NoError(t, err)
	require.Equal(t, "alpha\ngamma\nalpha\n", string(data))
}

func TestFileStorePreparePatchRejectsAmbiguousMatches(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "zero.txt"), "delta\ndelta\n")
	writeTestFile(t, filepath.Join(root, "twice.txt"), "beta\nbeta\n")
	store := NewFileStore(root, 64*1024)
	_, err := store.PreparePatch(context.Background(), "zero.txt", "beta", "gamma")
	require.ErrorIs(t, err, ErrPatchAmbiguous)
	_, err = store.PreparePatch(context.Background(), "twice.txt", "beta", "gamma")
	require.ErrorIs(t, err, ErrPatchAmbiguous)
	_, err = store.PreparePatch(context.Background(), "zero.txt", "", "gamma")
	require.ErrorIs(t, err, ErrPatchAmbiguous)
	_, err = store.PreparePatch(context.Background(), "missing.txt", "beta", "gamma")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFileStorePreparePatchApplyRejectsSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target.txt")
	writeTestFile(t, outside, "secret")
	writeTestFile(t, filepath.Join(root, "doc.txt"), "alpha\nbeta\nalpha\n")
	store := NewFileStore(root, 64*1024)
	change, err := store.PreparePatch(context.Background(), "doc.txt", "beta", "gamma")
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "doc.txt")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "doc.txt")))
	require.ErrorIs(t, change.Apply(context.Background()), ErrOutsideWorkspace)
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "secret", string(data))
}

func TestFileStoreDeleteOnlyRegularFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "dir"), 0o700))
	writeTestFile(t, filepath.Join(root, "file.txt"), "x")
	outside := filepath.Join(t.TempDir(), "target.txt")
	writeTestFile(t, outside, "secret")
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "outside-link.txt")))
	require.NoError(t, os.Symlink(
		filepath.Join(root, "file.txt"), filepath.Join(root, "inside-link.txt")))
	store := NewFileStore(root, 64*1024)
	_, err := store.Delete(context.Background(), "dir")
	require.ErrorIs(t, err, ErrNotDeletable)
	_, err = store.Delete(context.Background(), "missing.txt")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = store.Delete(context.Background(), "outside-link.txt")
	require.ErrorIs(t, err, ErrOutsideWorkspace)
	_, err = store.Delete(context.Background(), "inside-link.txt")
	require.ErrorIs(t, err, ErrNotDeletable)
}

func TestFileStoreDeleteRemovesFileAfterApply(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "notes.txt"), "x")
	store := NewFileStore(root, 64*1024)
	change, err := store.Delete(context.Background(), "notes.txt")
	require.NoError(t, err)
	require.Contains(t, change.Diff, "notes.txt")
	require.FileExists(t, filepath.Join(root, "notes.txt"))
	require.NoError(t, change.Apply(context.Background()))
	require.NoFileExists(t, filepath.Join(root, "notes.txt"))
	require.ErrorIs(t, change.Apply(context.Background()), ErrNotFound)
}

func TestFileStoreDeleteApplyRejectsSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target.txt")
	writeTestFile(t, outside, "secret")
	writeTestFile(t, filepath.Join(root, "file.txt"), "safe")
	store := NewFileStore(root, 64*1024)
	change, err := store.Delete(context.Background(), "file.txt")
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "file.txt")))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "file.txt")))
	require.ErrorIs(t, change.Apply(context.Background()), ErrOutsideWorkspace)
	require.FileExists(t, outside)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func padNumber(value int) string {
	digits := []byte{
		byte('0' + value/1000%10),
		byte('0' + value/100%10),
		byte('0' + value/10%10),
		byte('0' + value%10),
	}
	return string(digits)
}
