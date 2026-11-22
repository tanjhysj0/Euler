package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileStoreReadLinesPaging(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "code.txt"), "line1\nline2\nline3\nline4\nline5")
	store := NewFileStore(root, 64*1024)

	first, err := store.ReadLines(context.Background(), "code.txt", 1, 2)
	require.NoError(t, err)
	require.Equal(t, "line1\nline2", first.Content)
	require.Equal(t, 1, first.StartLine)
	require.Equal(t, 2, first.EndLine)
	require.True(t, first.HasMore)
	require.Equal(t, 3, first.NextOffset)

	second, err := store.ReadLines(context.Background(), "code.txt", first.NextOffset, 2)
	require.NoError(t, err)
	require.Equal(t, "line3\nline4", second.Content)
	require.Equal(t, 3, second.StartLine)
	require.Equal(t, 4, second.EndLine)
	require.True(t, second.HasMore)

	last, err := store.ReadLines(context.Background(), "code.txt", second.NextOffset, 2)
	require.NoError(t, err)
	require.Equal(t, "line5", last.Content)
	require.Equal(t, 5, last.EndLine)
	require.False(t, last.HasMore)
}

func TestFileStoreReadLinesStartsPastEndReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "short.txt"), "only")
	store := NewFileStore(root, 64*1024)
	page, err := store.ReadLines(context.Background(), "short.txt", 10, 5)
	require.NoError(t, err)
	require.Empty(t, page.Content)
	require.False(t, page.HasMore)
}

func TestFileStoreReadLinesReportsOverlongLine(t *testing.T) {
	root := t.TempDir()
	huge := strings.Repeat("x", maxLineBytes+1)
	writeTestFile(t, filepath.Join(root, "bundle.js"), huge)
	store := NewFileStore(root, 64*1024)
	_, err := store.ReadLines(context.Background(), "bundle.js", 1, 10)
	require.ErrorIs(t, err, ErrLineTooLong)
}

func TestFileStoreReadLinesKeepsEmptyLines(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "one\n\ntwo")
	store := NewFileStore(root, 64*1024)
	page, err := store.ReadLines(context.Background(), "a.txt", 1, 10)
	require.NoError(t, err)
	require.Equal(t, "one\n\ntwo", page.Content)
	require.Equal(t, 3, page.EndLine)
	require.False(t, page.HasMore)
}
