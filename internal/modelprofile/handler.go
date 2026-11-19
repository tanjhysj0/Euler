package modelprofile

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"euler/internal/api"
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
}

type createProfileRequest struct {
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	SecretKind string `json:"secret_kind"`
	SecretRef  string `json:"secret_ref"`
	APIKey     string `json:"api_key"`
}

func (h *Handler) create(c *gin.Context) {
	var request createProfileRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.AbortWithError(c, http.StatusBadRequest, "invalid_request", "请求体不是合法的 JSON")
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
		api.AbortWithError(c, http.StatusInternalServerError, "internal_error", "读取模型配置失败")
		return
	}
	c.JSON(http.StatusOK, profiles)
}

func abortCreateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrSecretRefInUse):
		api.AbortWithError(c, http.StatusBadRequest, "invalid_request", "该密钥引用已被占用")
	case errors.Is(err, ErrInvalidInput):
		api.AbortWithError(c, http.StatusBadRequest, "invalid_request", "模型配置参数无效")
	case errors.Is(err, ErrSecretUnavailable):
		api.AbortWithError(c, http.StatusBadRequest, "secret_unavailable", "无法访问本机密钥")
	default:
		api.AbortWithError(c, http.StatusInternalServerError, "internal_error", "创建模型配置失败")
	}
}
