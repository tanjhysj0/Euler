package session

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"euler/internal/api"
	"euler/internal/persistence"
	"euler/internal/workspace"
)

const (
	codeInvalidRequest       = "invalid_request"
	codeNotFound             = "not_found"
	codeInternalError        = "internal_error"
	codeWorkspaceUnavailable = "workspace_unavailable"
	codeModelProfileRequired = "model_profile_required"
	codeSessionHasActiveRun  = "session_has_active_run"
	defaultQueryInt          = 0
)

// WorkspaceView 是会话所关联的工作区。
type WorkspaceView struct {
	ID   uuid.UUID `json:"id"`
	Path string    `json:"path"`
}

// ModelView 是会话所关联的模型配置摘要，绝不包含任何密钥内容。
type ModelView struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	BaseURL string    `json:"base_url"`
	Model   string    `json:"model"`
}

// SessionView 是会话的 API 表示。
type SessionView struct {
	ID           uuid.UUID     `json:"id"`
	Title        string        `json:"title"`
	Status       string        `json:"status"`
	Workspace    WorkspaceView `json:"workspace"`
	Model        ModelView     `json:"model"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	LastActiveAt time.Time     `json:"last_active_at"`
}

// CreateResult 是新创建的会话及其工作区并发警告。
type CreateResult struct {
	SessionView
	WorkspaceWarning bool `json:"workspace_warning"`
}

// MessageView 是按会话序号排列的一条消息，parts 保存真实对话顺序。
type MessageView struct {
	ID          uuid.UUID              `json:"id"`
	RunID       *uuid.UUID             `json:"run_id,omitempty"`
	SessionSeq  int64                  `json:"session_seq"`
	Role        string                 `json:"role"`
	Content     string                 `json:"content"`
	Parts       []persistence.Part     `json:"parts"`
	ToolCalls   []persistence.ToolCall `json:"tool_calls,omitempty"`
	Status      string                 `json:"status"`
	CreatedAt   time.Time              `json:"created_at"`
	CompletedAt *time.Time             `json:"completed_at,omitempty"`
}

// MessagePage 是一页消息及下一页的游标。
type MessagePage struct {
	Messages  []MessageView `json:"messages"`
	NextAfter int64         `json:"next_after"`
}

// Handler 提供会话的 HTTP 端点。
type Handler struct {
	service *Service
}

// NewHandler 构建以 service 为后端的 Handler。
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Register 将会话路由挂载到已鉴权的 API 分组上。
func (h *Handler) Register(group *gin.RouterGroup) {
	group.POST("/sessions", h.create)
	group.GET("/sessions", h.list)
	group.GET("/sessions/:id", h.get)
	group.PATCH("/sessions/:id", h.update)
	group.DELETE("/sessions/:id", h.delete)
	group.GET("/sessions/:id/messages", h.messages)
}

type createSessionRequest struct {
	ProfileID uuid.UUID `json:"profile_id"`
	Path      string    `json:"path"`
}

// updateSessionRequest 支持切换模型或工作目录，二者必须且只能提供一个。
type updateSessionRequest struct {
	ModelProfileID uuid.UUID `json:"model_profile_id"`
	WorkspacePath  string    `json:"workspace_path"`
}

func (h *Handler) create(c *gin.Context) {
	var request createSessionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "请求体不是合法的 JSON")
		return
	}
	result, err := h.service.Create(c.Request.Context(), CreateInput(request))
	if err != nil {
		abortSessionError(c, err, "创建会话失败")
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *Handler) list(c *gin.Context) {
	filter := ListFilter{
		WorkspacePath: c.Query("workspace_path"),
		Query:         c.Query("q"),
	}
	activeRun, ok := parseActiveRun(c)
	if !ok {
		return
	}
	filter.ActiveRun = activeRun
	sessions, err := h.service.List(c.Request.Context(), filter)
	if err != nil {
		abortSessionError(c, err, "读取会话列表失败")
		return
	}
	c.JSON(http.StatusOK, sessions)
}

func (h *Handler) get(c *gin.Context) {
	id, ok := parseSessionID(c)
	if !ok {
		return
	}
	view, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		abortSessionError(c, err, "读取会话失败")
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *Handler) update(c *gin.Context) {
	id, ok := parseSessionID(c)
	if !ok {
		return
	}
	var request updateSessionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "请求体不是合法的 JSON")
		return
	}
	hasModel := request.ModelProfileID != uuid.Nil
	hasPath := request.WorkspacePath != ""
	if hasModel == hasPath {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest,
			"model_profile_id 与 workspace_path 必须且只能提供一个")
		return
	}
	var view SessionView
	var err error
	if hasPath {
		view, err = h.service.UpdateWorkspace(c.Request.Context(), id, request.WorkspacePath)
	} else {
		view, err = h.service.UpdateModel(c.Request.Context(), id, request.ModelProfileID)
	}
	if err != nil {
		abortSessionError(c, err, "更新会话失败")
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *Handler) delete(c *gin.Context) {
	id, ok := parseSessionID(c)
	if !ok {
		return
	}
	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		abortSessionError(c, err, "删除会话失败")
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) messages(c *gin.Context) {
	id, ok := parseSessionID(c)
	if !ok {
		return
	}
	after, ok := parseQueryInt(c, "after", defaultQueryInt)
	if !ok {
		return
	}
	limit, ok := parseQueryInt(c, "limit", defaultQueryInt)
	if !ok {
		return
	}
	page, err := h.service.Messages(c.Request.Context(), id, int64(after), limit)
	if err != nil {
		abortSessionError(c, err, "读取历史消息失败")
		return
	}
	c.JSON(http.StatusOK, page)
}

func parseActiveRun(c *gin.Context) (enabled bool, ok bool) {
	raw := c.Query("active_run")
	if raw == "" {
		return false, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "active_run 必须为布尔值")
		return false, false
	}
	return value, true
}

func parseQueryInt(c *gin.Context, key string, fallback int) (int, bool) {
	raw := c.Query(key)
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, key+" 必须为整数")
		return defaultQueryInt, false
	}
	return value, true
}

func parseSessionID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "会话 ID 无效")
		return uuid.Nil, false
	}
	return id, true
}

func abortSessionError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.AbortWithError(c, http.StatusNotFound, codeNotFound, "会话不存在")
	case errors.Is(err, ErrInvalidInput):
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "会话参数无效")
	case errors.Is(err, ErrNoModelProfile):
		api.AbortWithError(c, http.StatusBadRequest, codeModelProfileRequired,
			"请先在模型设置中创建模型配置")
	case errors.Is(err, ErrSessionHasActiveRun):
		api.AbortWithError(c, http.StatusConflict, codeSessionHasActiveRun,
			"会话有进行中的任务，请先停止")
	case errors.Is(err, workspace.ErrUnavailable):
		slog.Error("session workspace unavailable", "error", err)
		api.AbortWithError(c, http.StatusServiceUnavailable, codeWorkspaceUnavailable,
			"工作区目录不可用，请检查 workspace_root 配置与权限")
	default:
		slog.Error("session request failed", "error", err, "fallback", fallback)
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, fallback)
	}
}
