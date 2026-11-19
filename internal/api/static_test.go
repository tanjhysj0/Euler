package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"euler/web"
)

func newProductionRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	auth, err := NewLocalAuth(nil)
	require.NoError(t, err)
	router, _ := NewRouter(stubHealthChecker{}, auth)
	RegisterStatic(router, web.Dist())
	return router
}

func TestStaticSPAFallback(t *testing.T) {
	router := newProductionRouter(t)
	for _, path := range []string{"/", "/sessions/abc", "/settings"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), "<div id=\"root\"></div>")
	}
}

func TestStaticAssetsAreImmutable(t *testing.T) {
	router := newProductionRouter(t)
	assets, err := fs.Glob(web.Dist(), "assets/*")
	require.NoError(t, err)
	require.NotEmpty(t, assets)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/"+assets[0], nil)
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "public, max-age=31536000, immutable",
		response.Header().Get("Cache-Control"))
}

func TestStaticIndexIsNotCached(t *testing.T) {
	router := newProductionRouter(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "no-cache", response.Header().Get("Cache-Control"))
}

func TestStaticUnknownAPIStaysJSON(t *testing.T) {
	router := newProductionRouter(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent", nil)
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Contains(t, response.Header().Get("Content-Type"), "application/json")
	require.Equal(t, "not_found", decodeBody(t, response.Body.Bytes())["code"])
}

func TestStaticNonGetStaysJSON(t *testing.T) {
	router := newProductionRouter(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/sessions/abc", nil)
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Contains(t, response.Header().Get("Content-Type"), "application/json")
	require.Equal(t, "not_found", decodeBody(t, response.Body.Bytes())["code"])
}
