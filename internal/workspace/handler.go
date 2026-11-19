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
	// sniffLength 是传给 http.DetectContentType 的前缀长度。
	sniffLength = 512
	// decimalBase 是 Content-Length 数值使用的进制。
	decimalBase = 10
)

// Handler 提供工作区目录浏览相关的接口。
type Handler struct{}

// NewHandler 构建用于目录浏览的 Handler。
func NewHandler() *Handler {
	return &Handler{}
}

// Register 将目录浏览路由挂载到需要鉴权的 API 分组上。
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

// FileHandler 提供工作区文件树以及工作区产物的预览或下载接口。
type FileHandler struct {
	finder       WorkspaceFinder
	previewBytes int
}

// NewFileHandler 构建 FileHandler，通过 finder 解析工作区，
// 并将预览大小限制为 previewBytes。
func NewFileHandler(finder WorkspaceFinder, previewBytes int) *FileHandler {
	return &FileHandler{finder: finder, previewBytes: previewBytes}
}

// Register 将工作区产物路由挂载到需要鉴权的分组上。
func (h *FileHandler) Register(group *gin.RouterGroup) {
	group.GET("/workspaces/:id/files", h.files)
	group.GET("/workspaces/:id/files/content", h.content)
}

// files 返回工作区文件树，节点数上限为 maxListNodes。
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

// content 预览或下载单个工作区文件。预览内容受配置的大小限制约束；
// 下载则以流式方式传输完整文件。
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

// preview 写出受限大小的内容，并使用嗅探或修正后的媒体类型。
func (*FileHandler) preview(c *gin.Context, store *FileStore, requested string) {
	data, err := store.Read(c.Request.Context(), requested)
	if err != nil {
		abortFilesError(c, err, "读取工作区文件失败")
		return
	}
	c.Data(http.StatusOK, previewContentType(requested, data), data)
}

// download 以 attachment 方式流式传输完整文件。
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

// extensionContentTypes 为具有精确媒体类型的扩展名修正嗅探出的类型。
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

// previewContentType 嗅探前 512 字节，并对已知的文本和媒体扩展名
// 进行修正，使预览能够直接渲染而不是触发下载。
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

// downloadContentType 根据扩展名确定下载的 Content-Type，
// 默认使用二进制流类型，使未知类型按原样保存。
func downloadContentType(requested string) string {
	if known, ok := extensionContentTypes[filepath.Ext(requested)]; ok {
		return known
	}
	return "application/octet-stream"
}

// wantsDownload 将 download=true 视为下载请求；无法解析的值
// 回退为预览。
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

// abortFilesError 将工作区错误映射为统一的错误响应结构。
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
