package workspace

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"euler/internal/api"
)

const (
	codeInvalidRequest = "invalid_request"
	codeInvalidPath    = "invalid_path"
	codeNotFound       = "not_found"
	codePathOutside    = "path_outside_workspace"
	codeFileTooLarge   = "file_too_large"
	codeInternalError  = "internal_error"
	// sniffLength is the prefix length fed to http.DetectContentType.
	sniffLength = 512
	// decimalBase is the radix used for Content-Length values.
	decimalBase = 10
)

// Handler serves the workspace directory browsing endpoints.
type Handler struct{}

// NewHandler builds a Handler for directory browsing.
func NewHandler() *Handler {
	return &Handler{}
}

// Register mounts the directory browser routes on an authenticated API group.
func (*Handler) Register(group *gin.RouterGroup) {
	group.GET("/fs/directories", listDirectories)
}

func listDirectories(c *gin.Context) {
	listing, err := Browse(c.Query("path"))
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, "invalid_path", "目录路径无效或不可访问")
		return
	}
	c.JSON(http.StatusOK, listing)
}

// FileHandler serves the workspace file tree and the preview or download of
// workspace artifacts.
type FileHandler struct {
	finder       WorkspaceFinder
	previewBytes int
}

// NewFileHandler builds a FileHandler resolving workspaces through finder
// and bounding previews at previewBytes.
func NewFileHandler(finder WorkspaceFinder, previewBytes int) *FileHandler {
	return &FileHandler{finder: finder, previewBytes: previewBytes}
}

// Register mounts the workspace artifact routes on an authenticated group.
func (h *FileHandler) Register(group *gin.RouterGroup) {
	group.GET("/workspaces/:id/files", h.files)
	group.GET("/workspaces/:id/files/content", h.content)
}

// files returns the workspace file tree capped at maxListNodes nodes.
func (h *FileHandler) files(c *gin.Context) {
	id, ok := parseWorkspaceID(c)
	if !ok {
		return
	}
	root, err := h.finder.WorkspacePath(c.Request.Context(), id)
	if err != nil {
		abortFilesError(c, err, "读取工作区失败")
		return
	}
	nodes, err := NewFileStore(root, h.previewBytes).List(c.Request.Context())
	if err != nil {
		abortFilesError(c, err, "读取工作区文件树失败")
		return
	}
	c.JSON(http.StatusOK, nodes)
}

// content previews or downloads one workspace file. Preview payloads are
// bounded by the configured limit; downloads stream the complete file.
func (h *FileHandler) content(c *gin.Context) {
	id, ok := parseWorkspaceID(c)
	if !ok {
		return
	}
	requested := c.Query("path")
	if requested == "" {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidPath, "path 参数不能为空")
		return
	}
	root, err := h.finder.WorkspacePath(c.Request.Context(), id)
	if err != nil {
		abortFilesError(c, err, "读取工作区失败")
		return
	}
	store := NewFileStore(root, h.previewBytes)
	if wantsDownload(c) {
		h.download(c, store, requested)
		return
	}
	h.preview(c, store, requested)
}

// preview writes a limited payload with a sniffed or corrected media type.
func (*FileHandler) preview(c *gin.Context, store *FileStore, requested string) {
	data, err := store.Read(c.Request.Context(), requested)
	if err != nil {
		abortFilesError(c, err, "读取工作区文件失败")
		return
	}
	c.Data(http.StatusOK, previewContentType(requested, data), data)
}

// download streams the complete file with an attachment disposition.
func (*FileHandler) download(c *gin.Context, store *FileStore, requested string) {
	file, err := store.openReadFile(requested)
	if err != nil {
		abortFilesError(c, err, "读取工作区文件失败")
		return
	}
	if info, statErr := file.Stat(); statErr == nil && info.Mode().IsRegular() {
		c.Header("Content-Length", strconv.FormatInt(info.Size(), decimalBase))
	}
	disposition := mime.FormatMediaType(
		"attachment", map[string]string{"filename": filepath.Base(requested)})
	c.Header("Content-Disposition", disposition)
	c.Header("Content-Type", downloadContentType(requested))
	c.Status(http.StatusOK)
	_, copyErr := io.Copy(c.Writer, file)
	if closeErr := file.Close(); closeErr != nil {
		copyErr = errors.Join(copyErr, closeErr)
	}
	if copyErr != nil {
		// 响应头已写出，无法再改写状态码；记录截断供运维排查。
		slog.Warn("workspace download incomplete",
			"path", requested, "error", copyErr)
	}
}

// extensionContentTypes corrects sniffed types for extensions with precise
// media types.
var extensionContentTypes = map[string]string{
	".md":   "text/markdown; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".pdf":  "application/pdf",
}

// previewContentType sniffs the first 512 bytes and corrects known text and
// media extensions so previews render instead of downloading.
func previewContentType(requested string, data []byte) string {
	head := data
	if len(head) > sniffLength {
		head = head[:sniffLength]
	}
	if corrected, ok := extensionContentTypes[filepath.Ext(requested)]; ok {
		return corrected
	}
	return http.DetectContentType(head)
}

// downloadContentType resolves the download Content-Type from the extension,
// defaulting to a binary stream so unknown types are saved verbatim.
func downloadContentType(requested string) string {
	if known, ok := extensionContentTypes[filepath.Ext(requested)]; ok {
		return known
	}
	return "application/octet-stream"
}

// wantsDownload treats download=true as a download request; unparsable values
// fall back to preview.
func wantsDownload(c *gin.Context) bool {
	value, err := strconv.ParseBool(c.Query("download"))
	return err == nil && value
}

func parseWorkspaceID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "工作区 ID 无效")
		return uuid.Nil, false
	}
	return id, true
}

// abortFilesError maps workspace errors onto the unified error envelope.
func abortFilesError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, ErrWorkspaceNotFound):
		api.AbortWithError(c, http.StatusNotFound, codeNotFound, "工作区不存在")
	case errors.Is(err, ErrNotFound):
		api.AbortWithError(c, http.StatusNotFound, codeNotFound, "工作区文件不存在")
	case errors.Is(err, ErrOutsideWorkspace):
		api.AbortWithError(c, http.StatusBadRequest, codePathOutside, "路径不能逃逸出工作区")
	case errors.Is(err, ErrTooLarge):
		api.AbortWithError(c, http.StatusRequestEntityTooLarge, codeFileTooLarge, "文件超出预览大小限制")
	case errors.Is(err, ErrNotRegularFile):
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidPath, "路径不是普通文件")
	default:
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, fallback)
	}
}
