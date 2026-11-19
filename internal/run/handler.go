package run

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"euler/internal/api"
	"euler/internal/event"
)

const (
	codeInvalidRequest  = "invalid_request"
	codeNotFound        = "not_found"
	codeInternalError   = "internal_error"
	codeActiveRunExists = "active_run_exists"
)

// Handler exposes the run HTTP endpoints.
type Handler struct {
	manager *Manager
	store   *Store
	events  event.Publisher
}

// NewHandler builds a Handler backed by manager, store and events.
func NewHandler(manager *Manager, store *Store, events event.Publisher) *Handler {
	return &Handler{manager: manager, store: store, events: events}
}

// Register mounts the run routes on an authenticated API group.
func (h *Handler) Register(group *gin.RouterGroup) {
	group.POST("/sessions/:id/runs", h.start)
	group.GET("/runs/:id", h.get)
	group.POST("/runs/:id/cancel", h.cancel)
	group.GET("/runs/:id/events", api.SSEHandler(h.events, h.store))
}

type startRunRequest struct {
	Content string `json:"content"`
}

func (h *Handler) start(c *gin.Context) {
	sessionID, ok := parseIDParam(c, "会话 ID 无效")
	if !ok {
		return
	}
	var request startRunRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.AbortWithError(
			c, http.StatusBadRequest, codeInvalidRequest, "请求体不是合法的 JSON",
		)
		return
	}
	result, err := h.manager.Start(c.Request.Context(), sessionID, request.Content)
	if err != nil {
		abortRunError(c, err, "启动 Run 失败")
		return
	}
	c.JSON(http.StatusAccepted, result)
}

func (h *Handler) get(c *gin.Context) {
	runID, ok := parseIDParam(c, "Run ID 无效")
	if !ok {
		return
	}
	snapshot, err := h.store.Get(c.Request.Context(), runID)
	if err != nil {
		abortRunError(c, err, "读取 Run 失败")
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func (h *Handler) cancel(c *gin.Context) {
	runID, ok := parseIDParam(c, "Run ID 无效")
	if !ok {
		return
	}
	// State and its event must survive a client disconnect after the request.
	ctx := context.WithoutCancel(c.Request.Context())
	if err := h.manager.Cancel(ctx, runID); err != nil {
		abortRunError(c, err, "停止 Run 失败")
		return
	}
	snapshot, err := h.store.Get(ctx, runID)
	if err != nil {
		abortRunError(c, err, "读取 Run 失败")
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func parseIDParam(c *gin.Context, message string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, message)
		return uuid.Nil, false
	}
	return id, true
}

func abortRunError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.AbortWithError(c, http.StatusNotFound, codeNotFound, "会话或 Run 不存在")
	case errors.Is(err, ErrActiveRun):
		api.AbortWithError(c, http.StatusConflict, codeActiveRunExists, "该会话已有活跃 Run")
	case errors.Is(err, ErrInvalidInput):
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "Run 参数无效")
	default:
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, fallback)
	}
}
