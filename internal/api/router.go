// Package api 负责 Euler 后端的 HTTP 路由和处理器。
package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthChecker 报告后端依赖是否可达。
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// NewRouter 用目前已有的路由构建 HTTP router，并返回受保护的 /api/v1
// 分组，供需要认证的处理器挂载。
func NewRouter(checker HealthChecker, auth *LocalAuth) (*gin.Engine, *gin.RouterGroup) {
	router := gin.New()
	router.Use(gin.Recovery())
	RegisterHealth(router, checker)
	protected := RegisterLocalAuth(router, auth)
	return router, protected
}

// RegisterLocalAuth 挂载 bootstrap 端点，并返回由本机 cookie 和 Origin
// 检查保护的 /api/v1 分组。
func RegisterLocalAuth(router *gin.Engine, auth *LocalAuth) *gin.RouterGroup {
	router.GET("/api/v1/bootstrap", auth.Bootstrap())
	return router.Group("/api/v1", auth.Require(), auth.CheckOrigin())
}

// RegisterHealth 在 router 上挂载数据库健康检查端点。
func RegisterHealth(router *gin.Engine, checker HealthChecker) {
	router.GET("/health", func(c *gin.Context) {
		if err := checker.Ping(c.Request.Context()); err != nil {
			AbortWithError(c, http.StatusServiceUnavailable, "database_unavailable", "数据库不可用")
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}
