package approval

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"euler/internal/api"
)

const (
	// codeInvalidRequest 表示无法解析的审批 id 或决策请求体。
	codeInvalidRequest = "invalid_request"
	// codeMissingApproved 表示决策请求体缺少 approved 字段。
	codeMissingApproved = "missing_approved"
	// codeNotFound 表示未知的审批 id。
	codeNotFound = "not_found"
	// codeAlreadyDecided 表示对同一审批的第二次决策。
	codeAlreadyDecided = "approval_already_decided"
	// codeInternalError 表示意外的存储故障。
	codeInternalError = "internal_error"

	// fieldApproved 是请求体中的决策结果字段。
	fieldApproved = "approved"
)

// Handler 提供审批决策端点。
type Handler struct{ service *Service }

// NewHandler 基于一个服务构建审批处理器。
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// Register 将审批路由挂载到已鉴权的 API 分组上。
func (h *Handler) Register(group *gin.RouterGroup) {
	group.POST("/approvals/:id/decision", h.decide)
}

// decisionRequest 是 POST /approvals/{id}/decision 的请求体。Approved 是
// 指针，使省略该字段成为客户端错误，而不是被静默当作拒绝。
type decisionRequest struct {
	Approved *bool  `json:"approved"`
	Reason   string `json:"reason"`
}

// decide 记录一次决策。写入使用脱离请求的 context，因此客户端在
// UI 提交决策时断开连接，不会让审批永远停留在 pending 状态。
func (h *Handler) decide(c *gin.Context) {
	id, ok := parseApprovalID(c)
	if !ok {
		return
	}
	body, ok := parseDecision(c)
	if !ok {
		return
	}
	resolution := Resolution{
		Status:   StatusRejected,
		Approved: *body.Approved,
		Reason:   body.Reason,
	}
	if *body.Approved {
		resolution.Status = StatusApproved
	}
	record, err := h.service.Decide(context.WithoutCancel(c.Request.Context()), id, resolution)
	if err != nil {
		abortDecision(c, err)
		return
	}
	c.JSON(http.StatusOK, record)
}

func parseApprovalID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "审批 ID 无效")
		return uuid.Nil, false
	}
	return id, true
}

func parseDecision(c *gin.Context) (decisionRequest, bool) {
	var body decisionRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, "审批决策请求无效")
		return decisionRequest{}, false
	}
	if body.Approved == nil {
		api.AbortWithError(c, http.StatusBadRequest,
			codeMissingApproved, "请求体必须包含 "+fieldApproved+" 字段")
		return decisionRequest{}, false
	}
	return body, true
}

// abortDecision 将审批错误映射为统一的错误响应结构。
func abortDecision(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrAlreadyDecided):
		api.AbortWithError(c, http.StatusConflict, codeAlreadyDecided, "该审批已经决策")
	case errors.Is(err, ErrNotFound):
		api.AbortWithError(c, http.StatusNotFound, codeNotFound, "审批不存在")
	case errors.Is(err, ErrInvalidRequest):
		api.AbortWithError(c, http.StatusBadRequest, codeInvalidRequest, err.Error())
	default:
		api.AbortWithError(c, http.StatusInternalServerError, codeInternalError, "审批决策写入失败")
	}
}
