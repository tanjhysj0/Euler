package api

import "github.com/gin-gonic/gin"

// ErrorBody 是所有 API 处理器统一返回的 JSON 错误信封。
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// AbortWithError 中止处理器链，并以 ErrorBody 形式写出响应体。
func AbortWithError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorBody{Code: code, Message: message})
}
