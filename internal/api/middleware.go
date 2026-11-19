package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	localCookieName   = "euler_local"
	localTokenBytes   = 32
	sessionCookieAge  = 0
	constantTimeMatch = 1
)

// LocalAuth 通过进程级 cookie 保护本机 API，并对改变状态的请求做 Origin
// 检查。token 只存在于内存中。
type LocalAuth struct {
	token          string
	allowedOrigins map[string]struct{}
}

// NewLocalAuth 生成新的进程 token，并记录允许发送非幂等请求的额外
// origin。
func NewLocalAuth(allowedOrigins []string) (*LocalAuth, error) {
	raw := make([]byte, localTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate local token: %w", err)
	}
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = struct{}{}
	}
	return &LocalAuth{
		token:          base64.RawURLEncoding.EncodeToString(raw),
		allowedOrigins: allowed,
	}, nil
}

// Bootstrap 签发当前的本机 cookie。它是无副作用的 GET，因此持有旧进程
// token 的浏览器也能恢复访问。
func (a *LocalAuth) Bootstrap() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.SetSameSite(http.SameSiteStrictMode)
		c.SetCookie(localCookieName, a.token, sessionCookieAge, "/", "", false, true)
		c.Status(http.StatusNoContent)
	}
}

// Require 拒绝未携带当前进程 token 的请求。
func (a *LocalAuth) Require() gin.HandlerFunc {
	return func(c *gin.Context) {
		value, err := c.Cookie(localCookieName)
		if err == nil && a.tokenMatches(value) {
			c.Next()
			return
		}
		AbortWithError(c, http.StatusUnauthorized, "unauthorized", "缺少或无效的本机访问 Cookie")
	}
}

// CheckOrigin 拒绝跨站点的状态变更请求。安全方法以及不带 Origin 头的
// 请求直接放行。
func (a *LocalAuth) CheckOrigin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isSafeMethod(c.Request.Method) || a.originTrusted(c.Request) {
			c.Next()
			return
		}
		AbortWithError(c, http.StatusForbidden, "origin_forbidden", "请求来源不受信任")
	}
}

func (a *LocalAuth) tokenMatches(value string) bool {
	return subtle.ConstantTimeCompare([]byte(value), []byte(a.token)) == constantTimeMatch
}

func (a *LocalAuth) originTrusted(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" || origin == requestOrigin(request) {
		return true
	}
	_, ok := a.allowedOrigins[origin]
	return ok
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func requestOrigin(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + request.Host
}
