package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/api"
)

// fakeFinder resolves a single workspace ID to a fixed directory.
type fakeFinder struct {
	id   uuid.UUID
	path string
	err  error
}

func (f fakeFinder) WorkspacePath(_ context.Context, id uuid.UUID) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if id != f.id {
		return "", ErrWorkspaceNotFound
	}
	return f.path, nil
}

// newFileRouter mounts a FileHandler for finder with the given preview limit.
func newFileRouter(t *testing.T, finder WorkspaceFinder, previewBytes int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewFileHandler(finder, previewBytes).Register(router.Group("/api/v1"))
	return router
}

func newTestFinder(t *testing.T) (fakeFinder, string) {
	t.Helper()
	root := t.TempDir()
	return fakeFinder{id: uuid.New(), path: root}, root
}

func getFile(t *testing.T, router *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func fileBody(t *testing.T, recorder *httptest.ResponseRecorder) api.ErrorBody {
	t.Helper()
	var body api.ErrorBody
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func TestFileHandlerFilesReturnsTree(t *testing.T) {
	finder, root := newTestFinder(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.md"), []byte("hi"), 0o600))
	router := newFileRouter(t, finder, 1024)
	recorder := getFile(t, router, "/api/v1/workspaces/"+finder.id.String()+"/files")
	require.Equal(t, http.StatusOK, recorder.Code)
	var nodes []FileNode
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &nodes))
	paths := make([]string, len(nodes))
	for index, node := range nodes {
		paths[index] = node.Path
	}
	require.Contains(t, paths, "docs")
	require.Contains(t, paths, "notes.md")
}

func TestFileHandlerFilesErrors(t *testing.T) {
	known, _ := newTestFinder(t)
	router := newFileRouter(t, known, 1024)
	invalid := getFile(t, router, "/api/v1/workspaces/not-a-uuid/files")
	require.Equal(t, http.StatusBadRequest, invalid.Code)
	require.Equal(t, codeInvalidRequest, fileBody(t, invalid).Code)
	missing := getFile(t, router, "/api/v1/workspaces/"+uuid.New().String()+"/files")
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Equal(t, codeNotFound, fileBody(t, missing).Code)
}

func TestFileHandlerContentPreview(t *testing.T) {
	finder, root := newTestFinder(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.md"), []byte("# 标题"), 0o600))
	router := newFileRouter(t, finder, 1024)
	path := "/api/v1/workspaces/" + finder.id.String() + "/files/content?path=notes.md"
	recorder := getFile(t, router, path)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "text/markdown; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.Equal(t, "# 标题", recorder.Body.String())
	require.Empty(t, recorder.Header().Get("Content-Disposition"))
}

func TestFileHandlerContentPreviewTooLarge(t *testing.T) {
	finder, root := newTestFinder(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "big.txt"), []byte("abcdef"), 0o600))
	router := newFileRouter(t, finder, 4)
	recorder := getFile(
		t, router, "/api/v1/workspaces/"+finder.id.String()+"/files/content?path=big.txt",
	)
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Equal(t, codeFileTooLarge, fileBody(t, recorder).Code)
}

func TestFileHandlerContentDownload(t *testing.T) {
	finder, root := newTestFinder(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "image.png"), []byte("pngdata"), 0o600))
	router := newFileRouter(t, finder, 1024)
	path := "/api/v1/workspaces/" + finder.id.String() +
		"/files/content?path=image.png&download=true"
	recorder := getFile(t, router, path)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "attachment; filename=image.png",
		recorder.Header().Get("Content-Disposition"))
	require.Equal(t, "image/png", recorder.Header().Get("Content-Type"))
	require.Equal(t, "pngdata", recorder.Body.String())
}

func TestFileHandlerContentErrors(t *testing.T) {
	finder, root := newTestFinder(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.md"), []byte("hi"), 0o600))
	router := newFileRouter(t, finder, 1024)
	base := "/api/v1/workspaces/" + finder.id.String() + "/files/content"
	missingParam := getFile(t, router, base)
	require.Equal(t, http.StatusBadRequest, missingParam.Code)
	require.Equal(t, codeInvalidPath, fileBody(t, missingParam).Code)
	escaped := getFile(t, router, base+"?path=../secret.txt")
	require.Equal(t, http.StatusBadRequest, escaped.Code)
	require.Equal(t, codePathOutside, fileBody(t, escaped).Code)
	missingFile := getFile(t, router, base+"?path=nope.txt")
	require.Equal(t, http.StatusNotFound, missingFile.Code)
	require.Equal(t, codeNotFound, fileBody(t, missingFile).Code)
	workspaceMissing := getFile(t, router, "/api/v1/workspaces/"+uuid.New().String()+
		"/files/content?path=notes.md")
	require.Equal(t, http.StatusNotFound, workspaceMissing.Code)
	require.Equal(t, codeNotFound, fileBody(t, workspaceMissing).Code)
}

func TestFileHandlerContentSniffsUnknownExtension(t *testing.T) {
	finder, root := newTestFinder(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "data.bin"), []byte{0x00, 0x01}, 0o600))
	router := newFileRouter(t, finder, 1024)
	recorder := getFile(
		t, router, "/api/v1/workspaces/"+finder.id.String()+"/files/content?path=data.bin",
	)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, strings.HasPrefix(
		recorder.Header().Get("Content-Type"), "application/octet-stream"),
		recorder.Header().Get("Content-Type"))
}

func TestFakeFinderPropagatesErrors(t *testing.T) {
	finder := fakeFinder{err: errors.New("boom")}
	_, err := finder.WorkspacePath(context.Background(), uuid.New())
	require.EqualError(t, err, "boom")
}
