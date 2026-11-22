package modelprofile

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"euler/internal/api"
)

const (
	codeInvalidRequest       = "invalid_request"
	codeInternalError        = "internal_error"
	codeNotFound             = "not_found"
	codeSecretUnavailable    = "secret_unavailable"
	codeLastModelProfile     = "last_model_profile"
	codeModelListUnavailable = "model_list_unavailable"
)

// Handler 提供模型配置的 HTTP 接口。
type Handler struct {
	service *Service
}

// NewHandler 构建由 service 支撑的 Handler。
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Register 将模型配置路由挂载到经过认证的 API 分组上。
func (h *Handler) Register(group *gin.RouterGroup) {
	group.GET("/model-profiles", h.list)
	group.POST("/model-profiles", h.create)
	group.POST("/model-profiles/models", h.listModels)
	group.PUT("/model-profiles/:id", h.update)
	group.DELETE("/model-profiles/:id", h.delete)
}

type createProfileRequest struct {
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	SecretKind string `json:"secret_kind"`
	SecretRef  string `json:"secret_ref"`
	APIKey     string `json:"api_key"`
}

type updateProfileRequest struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	SecretRef string `json:"secret_ref"`
	APIKey    string `json:"api_key"`
}

type listModelsRequest struct {
	BaseURL    string    `json:"base_url"`
	APIKey     string    `json:"api_key"`
	SecretKind string    `json:"secret_kind"`
	SecretRef  string    `json:"secret_ref"`
	ProfileID  uuid.UUID `json:"profile_id"`
}

func (h *Handler) create(c *gin.Context) {
	var request createProfileRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "请求体不是合法的 JSON")
		return
	}
	profile, err := h.service.Create(c.Request.Context(), CreateInput(request))
	if err != nil {
		abortCreateError(c, err)
		return
	}
	c.JSON(http.StatusCreated, profile)
}

func (h *Handler) list(c *gin.Context) {
	profiles, err := h.service.List(c.Request.Context())
	if err != nil {
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, "读取模型配置失败")
		return
	}
	c.JSON(http.StatusOK, profiles)
}

func (h *Handler) listModels(c *gin.Context) {
	var request listModelsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "请求体不是合法的 JSON")
		return
	}
	models, err := h.service.ListModels(c.Request.Context(), listModelsInput(request))
	if err != nil {
		abortListModelsError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

func (h *Handler) update(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "模型配置 ID 无效")
		return
	}
	var request updateProfileRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "请求体不是合法的 JSON")
		return
	}
	profile, err := h.service.Update(c.Request.Context(), id, updateInput(request))
	if err != nil {
		abortUpdateError(c, err)
		return
	}
	c.JSON(http.StatusOK, profile)
}

func (h *Handler) delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "模型配置 ID 无效")
		return
	}
	rebound, err := h.service.Delete(c.Request.Context(), id)
	if err != nil {
		abortDeleteError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"rebound_sessions": rebound})
}

func abortCreateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrSecretRefInUse):
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "该密钥引用已被占用")
	case errors.Is(err, ErrInvalidInput):
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "模型配置参数无效")
	case errors.Is(err, ErrSecretUnavailable):
		api.AbortWithError(c, http.StatusBadRequest, codeSecretUnavailable, "无法访问本机密钥")
	default:
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, "创建模型配置失败")
	}
}

func abortUpdateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.AbortWithError(c, http.StatusNotFound, codeNotFound, "模型配置不存在")
	case errors.Is(err, ErrInvalidInput):
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "模型配置参数无效")
	case errors.Is(err, ErrSecretUnavailable):
		api.AbortWithError(c, http.StatusBadRequest, codeSecretUnavailable, "无法访问本机密钥")
	default:
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, "更新模型配置失败")
	}
}

func abortDeleteError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.AbortWithError(c, http.StatusNotFound, codeNotFound, "模型配置不存在")
	case errors.Is(err, ErrLastProfile):
		api.AbortWithError(c, http.StatusConflict, codeLastModelProfile, "至少保留一个模型配置")
	default:
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, "删除模型配置失败")
	}
}

func abortListModelsError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "Base URL 与 API Key 不能为空")
	case errors.Is(err, ErrSecretUnavailable):
		api.AbortWithError(c, http.StatusBadRequest, codeSecretUnavailable, "无法读取模型密钥")
	case errors.Is(err, ErrModelListUnavailable):
		api.AbortWithError(c, http.StatusBadGateway, codeModelListUnavailable,
			"无法获取模型列表，请检查 Base URL 与 API Key")
	default:
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, "获取模型列表失败")
	}
}
