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

// LocalAuth guards the local API with a per-process cookie and an Origin check
// for state-changing requests. The token only lives in memory.
type LocalAuth struct {
	token          string
	allowedOrigins map[string]struct{}
}

// NewLocalAuth generates a fresh process token and records the extra origins
// that may send non-idempotent requests.
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

// Bootstrap issues the current local cookie. It is a side-effect-free GET so a
// browser holding a token from a previous process can recover.
func (a *LocalAuth) Bootstrap() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.SetSameSite(http.SameSiteStrictMode)
		c.SetCookie(localCookieName, a.token, sessionCookieAge, "/", "", false, true)
		c.Status(http.StatusNoContent)
	}
}

// Require rejects requests that do not carry the current process token.
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

// CheckOrigin rejects cross-site state-changing requests. Safe methods and
// requests without an Origin header pass through.
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
