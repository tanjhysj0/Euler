package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type stubHealthChecker struct {
	err error
}

func (s stubHealthChecker) Ping(_ context.Context) error {
	return s.err
}

func TestHealthEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name     string
		pingErr  error
		wantCode int
		wantBody map[string]string
	}{
		{
			name:     "healthy database",
			wantCode: http.StatusOK,
			wantBody: map[string]string{"status": "ok"},
		},
		{
			name:     "unavailable database",
			pingErr:  errors.New("connection refused"),
			wantCode: http.StatusServiceUnavailable,
			wantBody: map[string]string{
				"code":    "database_unavailable",
				"message": "数据库不可用",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			auth, err := NewLocalAuth(nil)
			if err != nil {
				t.Fatalf("new local auth: %v", err)
			}
			router, _ := NewRouter(stubHealthChecker{err: testCase.pingErr}, auth)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/health", nil)
			router.ServeHTTP(recorder, request)
			if recorder.Code != testCase.wantCode {
				t.Fatalf("status=%d want=%d", recorder.Code, testCase.wantCode)
			}
			body := decodeBody(t, recorder.Body.Bytes())
			for key, want := range testCase.wantBody {
				if body[key] != want {
					t.Fatalf("body[%q]=%q want=%q", key, body[key], want)
				}
			}
		})
	}
}

func decodeBody(t *testing.T, data []byte) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return body
}
