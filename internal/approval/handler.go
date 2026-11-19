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
	// codeInvalidRequest marks an unparsable approval id or decision body.
	codeInvalidRequest = "invalid_request"
	// codeMissingApproved marks a decision body without the approved field.
	codeMissingApproved = "missing_approved"
	// codeNotFound marks an unknown approval id.
	codeNotFound = "not_found"
	// codeAlreadyDecided marks the second decision for one approval.
	codeAlreadyDecided = "approval_already_decided"
	// codeInternalError marks an unexpected storage failure.
	codeInternalError = "internal_error"

	// fieldApproved is the decision outcome field of the request body.
	fieldApproved = "approved"
)

// Handler serves the approval decision endpoint.
type Handler struct{ service *Service }

// NewHandler builds the approval handler over one service.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// Register mounts the approval routes on an authenticated API group.
func (h *Handler) Register(group *gin.RouterGroup) {
	group.POST("/approvals/:id/decision", h.decide)
}

// decisionRequest is the body of POST /approvals/{id}/decision. Approved is a
// pointer so an omitted field is a client error instead of a silent rejection.
type decisionRequest struct {
	Approved *bool  `json:"approved"`
	Reason   string `json:"reason"`
}

// decide records one decision. The write uses a detached context so a client
// that disconnects while the UI submits the decision cannot leave the approval
// pending forever.
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

// abortDecision maps approval errors onto the unified error envelope.
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
