package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	staticIndex    = "index.html"
	assetsPrefix   = "/assets/"
	immutableCache = "public, max-age=31536000, immutable"
)

// RegisterStatic 在未匹配的路由上从 dist 提供内嵌的 SPA。
// API 和 health 路径绝不回退到 HTML，确保客户端始终收到
// JSON 错误信封。
func RegisterStatic(router *gin.Engine, dist fs.FS) {
	router.NoRoute(func(c *gin.Context) { serveStatic(c, dist) })
}

func serveStatic(c *gin.Context, dist fs.FS) {
	path := c.Request.URL.Path
	if !isStaticRequest(c.Request.Method, path) {
		AbortWithError(c, http.StatusNotFound, "not_found", "接口不存在")
		return
	}
	name := strings.TrimPrefix(path, "/")
	if name == "" || name == staticIndex {
		serveIndex(c, dist)
		return
	}
	info, err := fs.Stat(dist, name)
	if err != nil || !info.Mode().IsRegular() {
		serveIndex(c, dist)
		return
	}
	if strings.HasPrefix(path, assetsPrefix) {
		c.Header("Cache-Control", immutableCache)
	}
	http.ServeFileFS(c.Writer, c.Request, dist, name)
}

// isStaticRequest 只允许浏览器导航（API 和 health 命名空间之外的
// GET/HEAD）进入 SPA 回退。
func isStaticRequest(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	return !strings.HasPrefix(path, "/api") && !strings.HasPrefix(path, "/health")
}

func serveIndex(c *gin.Context, dist fs.FS) {
	c.Header("Cache-Control", "no-cache")
	http.ServeFileFS(c.Writer, c.Request, dist, staticIndex)
}
