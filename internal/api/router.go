// Package api owns the HTTP router and handlers for the Euler backend.
package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthChecker reports whether a backing dependency is reachable.
type HealthChecker interface {
	Ping(ctx context.Context) error
}

// NewRouter builds the HTTP router with the routes available so far and
// returns the guarded /api/v1 group that authenticated handlers attach to.
func NewRouter(checker HealthChecker, auth *LocalAuth) (*gin.Engine, *gin.RouterGroup) {
	router := gin.New()
	router.Use(gin.Recovery())
	RegisterHealth(router, checker)
	protected := RegisterLocalAuth(router, auth)
	return router, protected
}

// RegisterLocalAuth mounts the bootstrap endpoint and returns the /api/v1
// group guarded by the local cookie and Origin checks.
func RegisterLocalAuth(router *gin.Engine, auth *LocalAuth) *gin.RouterGroup {
	router.GET("/api/v1/bootstrap", auth.Bootstrap())
	return router.Group("/api/v1", auth.Require(), auth.CheckOrigin())
}

// RegisterHealth mounts the database health endpoint on router.
func RegisterHealth(router *gin.Engine, checker HealthChecker) {
	router.GET("/health", func(c *gin.Context) {
		if err := checker.Ping(c.Request.Context()); err != nil {
			AbortWithError(c, http.StatusServiceUnavailable, "database_unavailable", "数据库不可用")
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
}
