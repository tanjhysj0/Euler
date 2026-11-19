package modelprofile

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const testAPIKey = "sk-handler-secret"

func TestHandlerCreatesKeychainProfileWithoutEcho(t *testing.T) {
	secrets := newFakeSecretStore()
	router := newProfileRouter(t, NewService(newFakeProfileRepository(), secrets))
	body := `{"name":"test","base_url":"https://api.example/v1","model":"demo",` +
		`"secret_kind":"keychain","secret_ref":"euler/test","api_key":"` + testAPIKey + `"}`
	request := newJSONRequest(http.MethodPost, body)
	recorder := performJSON(t, router, request)
	require.Equal(t, http.StatusCreated, recorder.Code)
	require.NotContains(t, recorder.Body.String(), testAPIKey)
	require.NotContains(t, recorder.Body.String(), "api_key")
	require.Equal(t, testAPIKey, secrets.values["euler/test"])

	var profile Profile
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &profile))
	require.NotEqual(t, uuid.Nil, profile.ID)
	require.Equal(t, "test", profile.Name)
	require.Equal(t, SecretKindKeychain, profile.SecretKind)
}

func TestHandlerRejectsInvalidInput(t *testing.T) {
	router := newProfileRouter(t, NewService(newFakeProfileRepository(), newFakeSecretStore()))
	body := `{"name":"","base_url":"https://api.example/v1","model":"demo",` +
		`"secret_kind":"environment","secret_ref":"EULER_TEST_MODEL_KEY"}`
	request := newJSONRequest(http.MethodPost, body)
	recorder := performJSON(t, router, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request")
}

func TestHandlerReportsSecretUnavailable(t *testing.T) {
	secrets := newFakeSecretStore()
	secrets.setErr = errors.New("keychain locked")
	router := newProfileRouter(t, NewService(newFakeProfileRepository(), secrets))
	body := `{"name":"test","base_url":"https://api.example/v1","model":"demo",` +
		`"secret_kind":"keychain","secret_ref":"euler/test","api_key":"` + testAPIKey + `"}`
	request := newJSONRequest(http.MethodPost, body)
	recorder := performJSON(t, router, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "secret_unavailable")
	require.NotContains(t, recorder.Body.String(), testAPIKey)
}

func TestHandlerReportsSecretRefInUse(t *testing.T) {
	secrets := newFakeSecretStore()
	secrets.values["euler/existing"] = "sk-original"
	router := newProfileRouter(t, NewService(newFakeProfileRepository(), secrets))
	body := `{"name":"test","base_url":"https://api.example/v1","model":"demo",` +
		`"secret_kind":"keychain","secret_ref":"euler/existing","api_key":"` + testAPIKey + `"}`
	request := newJSONRequest(http.MethodPost, body)
	recorder := performJSON(t, router, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "已被占用")
	require.NotContains(t, recorder.Body.String(), "sk-original")
	require.NotContains(t, recorder.Body.String(), testAPIKey)
}

func TestHandlerListsProfiles(t *testing.T) {
	router := newProfileRouter(t, NewService(newFakeProfileRepository(), newFakeSecretStore()))
	request := newJSONRequest(http.MethodGet, "")
	recorder := performJSON(t, router, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	var profiles []Profile
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &profiles))
	require.Len(t, profiles, 1)
	require.Equal(t, "EULER_TEST_MODEL_KEY", profiles[0].SecretRef)
}

func newProfileRouter(t *testing.T, svc *Service) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(svc).Register(router.Group("/api/v1"))
	return router
}

const profilesPath = "/api/v1/model-profiles"

func newJSONRequest(method, body string) *http.Request {
	request := httptest.NewRequest(method, profilesPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func performJSON(
	t *testing.T, router http.Handler, request *http.Request,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
