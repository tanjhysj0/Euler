package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const testAllowedOrigin = "http://127.0.0.1:5173"

func TestBootstrapCookieAndOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	auth, err := NewLocalAuth([]string{"http://127.0.0.1:5173"})
	require.NoError(t, err)
	router := gin.New()
	router.GET("/api/v1/bootstrap", auth.Bootstrap())
	protected := router.Group("/api/v1", auth.Require(), auth.CheckOrigin())
	protected.POST("/sessions", func(c *gin.Context) { c.Status(http.StatusCreated) })

	boot := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	router.ServeHTTP(boot, req)
	require.Equal(t, http.StatusNoContent, boot.Code)
	cookies := boot.Result().Cookies()
	require.Len(t, cookies, 1)
	require.True(t, cookies[0].HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)

	denied := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	req.AddCookie(cookies[0])
	req.Header.Set("Origin", "https://evil.example")
	router.ServeHTTP(denied, req)
	require.Equal(t, http.StatusForbidden, denied.Code)
}

func TestRequireRejectsMissingCookie(t *testing.T) {
	router := newGuardRouter(t, testAllowedOrigin)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	request.Header.Set("Origin", testAllowedOrigin)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	var body ErrorBody
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "unauthorized", body.Code)
	require.NotEmpty(t, body.Message)
}

func TestCheckOriginAllowsTrustedWrites(t *testing.T) {
	router := newGuardRouter(t, testAllowedOrigin)
	cookie := bootstrapCookie(t, router)
	cases := []struct {
		name   string
		origin string
	}{
		{name: "same origin", origin: "http://example.com"},
		{name: "configured origin", origin: testAllowedOrigin},
		{name: "no origin header", origin: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
			request.AddCookie(cookie)
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusCreated, recorder.Code)
		})
	}
}

func TestBootstrapReplacesStaleCookie(t *testing.T) {
	router := newGuardRouter(t, testAllowedOrigin)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	request.AddCookie(&http.Cookie{Name: localCookieName, Value: "stale-token"})
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	cookies := recorder.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, localCookieName, cookies[0].Name)
	require.NotEqual(t, "stale-token", cookies[0].Value)
	require.Equal(t, "/", cookies[0].Path)
	require.False(t, cookies[0].Secure)

	allowed := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	request.AddCookie(cookies[0])
	request.Header.Set("Origin", "http://example.com")
	router.ServeHTTP(allowed, request)
	require.Equal(t, http.StatusCreated, allowed.Code)
}

func TestRouterGuardsAPIGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	auth, err := NewLocalAuth([]string{testAllowedOrigin})
	require.NoError(t, err)
	router, protected := NewRouter(stubHealthChecker{}, auth)
	protected.POST("/probe", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	denied := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/probe", nil)
	request.Header.Set("Origin", testAllowedOrigin)
	router.ServeHTTP(denied, request)
	require.Equal(t, http.StatusUnauthorized, denied.Code)

	cookie := bootstrapCookie(t, router)
	allowed := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/probe", nil)
	request.AddCookie(cookie)
	request.Header.Set("Origin", testAllowedOrigin)
	router.ServeHTTP(allowed, request)
	require.Equal(t, http.StatusNoContent, allowed.Code)

	forbidden := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/probe", nil)
	request.AddCookie(cookie)
	request.Header.Set("Origin", "https://evil.example")
	router.ServeHTTP(forbidden, request)
	require.Equal(t, http.StatusForbidden, forbidden.Code)
}

func newGuardRouter(t *testing.T, allowedOrigins ...string) *gin.Engine {
	t.Helper()
	auth, err := NewLocalAuth(allowedOrigins)
	require.NoError(t, err)
	router := gin.New()
	router.GET("/api/v1/bootstrap", auth.Bootstrap())
	protected := router.Group("/api/v1", auth.Require(), auth.CheckOrigin())
	protected.POST("/sessions", func(c *gin.Context) { c.Status(http.StatusCreated) })
	return router
}

func bootstrapCookie(t *testing.T, router http.Handler) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	router.ServeHTTP(recorder, request)
	cookies := recorder.Result().Cookies()
	require.Len(t, cookies, 1)
	return cookies[0]
}
