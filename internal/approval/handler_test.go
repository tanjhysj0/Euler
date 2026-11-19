package approval

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newApprovalRouter(service *Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHandler(service).Register(router.Group("/api/v1"))
	return router
}

func performDecision(
	t *testing.T,
	router *gin.Engine,
	id, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/approvals/"+id+"/decision", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// seedApproval 直接向 fake store 插入一行审批。
func seedApproval(store *fakeStore, status string) Record {
	record := Record{
		ID:          uuid.New(),
		RunID:       uuid.New(),
		ToolCallID:  uuid.New(),
		Status:      status,
		RequestedAt: time.Now().UTC(),
	}
	store.records[record.ID] = record
	return record
}

func TestHandlerDecidesApproved(t *testing.T) {
	store := newFakeStore()
	events := &fakeBroadcaster{}
	router := newApprovalRouter(NewService(store, events))
	seeded := seedApproval(store, StatusPending)
	recorder := performDecision(t, router, seeded.ID.String(),
		`{"approved":true,"reason":"ok"}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	var record Record
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &record))
	require.Equal(t, StatusApproved, record.Status)
	require.True(t, record.Decision.Approved)
	require.Equal(t, "ok", record.Decision.Reason)
	require.NotNil(t, record.DecidedAt)
	require.Equal(t, []string{EventResolved}, events.types())
}

func TestHandlerDecidesRejected(t *testing.T) {
	store := newFakeStore()
	router := newApprovalRouter(NewService(store, &fakeBroadcaster{}))
	seeded := seedApproval(store, StatusPending)
	recorder := performDecision(t, router, seeded.ID.String(),
		`{"approved":false,"reason":"当前目录不允许执行该命令"}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	var record Record
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &record))
	require.Equal(t, StatusRejected, record.Status)
	require.False(t, record.Decision.Approved)
	require.Equal(t, "当前目录不允许执行该命令", record.Decision.Reason)
}

func TestHandlerRejectsInvalidID(t *testing.T) {
	router := newApprovalRouter(NewService(newFakeStore(), &fakeBroadcaster{}))
	recorder := performDecision(t, router, "not-a-uuid", `{"approved":true}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request")
}

func TestHandlerRejectsMalformedBody(t *testing.T) {
	store := newFakeStore()
	router := newApprovalRouter(NewService(store, &fakeBroadcaster{}))
	seeded := seedApproval(store, StatusPending)
	recorder := performDecision(t, router, seeded.ID.String(), `{`)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid_request")
}

func TestHandlerRejectsMissingApproved(t *testing.T) {
	store := newFakeStore()
	router := newApprovalRouter(NewService(store, &fakeBroadcaster{}))
	seeded := seedApproval(store, StatusPending)
	recorder := performDecision(t, router, seeded.ID.String(), `{"reason":"x"}`)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "missing_approved")
	require.Equal(t, StatusPending, store.records[seeded.ID].Status)
}

func TestHandlerConflictsOnSecondDecision(t *testing.T) {
	store := newFakeStore()
	router := newApprovalRouter(NewService(store, &fakeBroadcaster{}))
	seeded := seedApproval(store, StatusApproved)
	recorder := performDecision(t, router, seeded.ID.String(), `{"approved":false}`)
	require.Equal(t, http.StatusConflict, recorder.Code)
	require.Contains(t, recorder.Body.String(), "approval_already_decided")
}

func TestHandlerReportsUnknownApproval(t *testing.T) {
	router := newApprovalRouter(NewService(newFakeStore(), &fakeBroadcaster{}))
	recorder := performDecision(t, router, uuid.New().String(), `{"approved":true}`)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.Contains(t, recorder.Body.String(), "not_found")
}

func TestHandlerReportsStorageFailure(t *testing.T) {
	store := newFakeStore()
	store.resolveErr = errors.New("database down")
	router := newApprovalRouter(NewService(store, &fakeBroadcaster{}))
	seeded := seedApproval(store, StatusPending)
	recorder := performDecision(t, router, seeded.ID.String(), `{"approved":true}`)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "internal_error")
}
