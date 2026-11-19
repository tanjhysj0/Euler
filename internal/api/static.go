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

// RegisterStatic serves the embedded SPA from dist on unmatched routes.
// API and health paths never fall back to HTML so clients always receive
// the JSON error envelope.
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

// isStaticRequest only lets browser navigation (GET/HEAD outside the API and
// health namespaces) reach the SPA fallback.
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
