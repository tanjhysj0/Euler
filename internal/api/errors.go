package api

import "github.com/gin-gonic/gin"

// ErrorBody is the unified JSON error envelope returned by every API handler.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// AbortWithError stops the handler chain and writes body as an ErrorBody.
func AbortWithError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorBody{Code: code, Message: message})
}
