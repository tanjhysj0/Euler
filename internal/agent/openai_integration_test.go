//go:build integration

package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"euler/internal/modelprofile"
	"euler/internal/persistence"
	"euler/internal/tools"
)

const sseChunkTemplate = `{"id":"chatcmpl-1","object":"chat.completion.chunk",` +
	`"created":1700000000,"model":"test-model","choices":[{"index":0,"delta":%s,` +
	`"finish_reason":%s}]}`

func TestOpenAIRuntimeToolLoop(t *testing.T) {
	fixture := newRuntimeFixture(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(fixture.dir, "note.txt"), []byte("你好"), 0o600))
	server := &openAIServer{t: t, mode: "tool_loop"}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handle))
	t.Cleanup(httpServer.Close)
	runtime := NewRuntime(openAIDeps(t, fixture, httpServer.URL))
	require.NoError(t, runtime.Run(context.Background(), fixture.request))
	require.Equal(t, 2, server.requestCount())
	second := server.bodyAt(t, 1)
	require.Contains(t, second, `"role":"tool"`)
	require.Contains(t, second, "你好")
	var message persistence.Message
	require.NoError(t, fixture.db.First(&message,
		"id = ?", fixture.request.AssistantMessageID).Error)
	require.Equal(t, "你好，世界", message.Content)
	require.Equal(t, int64(1), countAgentEvents(t, fixture.db, fixture.request.RunID,
		"tool.completed"))
	require.NotEmpty(t, agentEventPayloads(t, fixture.db, fixture.request.RunID,
		"message.delta"))
}

func TestOpenAIRuntimeMapsAuthError(t *testing.T) {
	fixture := newRuntimeFixture(t)
	server := &openAIServer{t: t, mode: "auth"}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handle))
	t.Cleanup(httpServer.Close)
	runtime := NewRuntime(openAIDeps(t, fixture, httpServer.URL))
	err := runtime.Run(context.Background(), fixture.request)
	require.Error(t, err)
	var coded *tools.CodedError
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "model_auth", coded.ErrorCode())
	require.Equal(t, 1, server.requestCount())
}

func TestOpenAIRuntimeMapsMidStreamError(t *testing.T) {
	fixture := newRuntimeFixture(t)
	server := &openAIServer{t: t, mode: "broken_stream"}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handle))
	t.Cleanup(httpServer.Close)
	runtime := NewRuntime(openAIDeps(t, fixture, httpServer.URL))
	err := runtime.Run(context.Background(), fixture.request)
	require.Error(t, err)
	var coded *tools.CodedError
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "model_protocol", coded.ErrorCode())
	require.NotContains(t, err.Error(), "sk-openai-test")
	require.Contains(t, err.Error(), "[REDACTED]")
}

func TestOpenAIRuntimeCancel(t *testing.T) {
	fixture := newRuntimeFixture(t)
	server := &openAIServer{t: t, mode: "hang", started: make(chan struct{})}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handle))
	t.Cleanup(httpServer.Close)
	runtime := NewRuntime(openAIDeps(t, fixture, httpServer.URL))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Run(ctx, fixture.request)
	}()
	select {
	case <-server.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model request never reached the server")
	}
	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
}

func openAIDeps(t *testing.T, fixture *runtimeFixture, baseURL string) Deps {
	t.Helper()
	deps := fixture.deps(t, nil)
	deps.Factory = nil
	deps.Profiles = staticResolver{profile: modelprofile.ResolvedProfile{
		BaseURL: baseURL, Model: "test-model", APIKey: "sk-openai-test",
	}}
	return deps
}

// openAIServer 在本地模拟 OpenAI 的 chat/completions 端点；
// 任何请求都不会离开 loopback 接口。
type openAIServer struct {
	t        *testing.T
	mu       sync.Mutex
	bodies   []string
	mode     string
	requests int
	started  chan struct{}
}

func (s *openAIServer) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.bodies = append(s.bodies, string(body))
	s.requests++
	count := s.requests
	s.mu.Unlock()
	switch s.mode {
	case "auth":
		w.WriteHeader(http.StatusUnauthorized)
		s.writeBody(w, `{"error":{"message":"invalid key","type":"invalid_request_error"}}`)
	case "broken_stream":
		w.Header().Set("Content-Type", "text/event-stream")
		s.writeSSEChunk(w, `{"role":"assistant","content":"partial"}`, "null")
		s.writeBody(w, "data: {\"error\":{\"message\":\"upstream failed for key "+
			"sk-openai-test\",\"type\":\"server_error\"}}\n\n")
	case "hang":
		if count == 1 {
			close(s.started)
		}
		<-r.Context().Done()
	default:
		s.streamToolLoop(w, count)
	}
}

func (s *openAIServer) streamToolLoop(w http.ResponseWriter, count int) {
	w.Header().Set("Content-Type", "text/event-stream")
	if count == 1 {
		s.writeSSEChunk(w, `{"role":"assistant"}`, "null")
		s.writeSSEChunk(w, `{"tool_calls":[{"index":0,"id":"call_1","type":"function",`+
			`"function":{"name":"file_read","arguments":"{\"path\":\"note.txt\"}"}}]}`,
			"null")
		s.writeSSEChunk(w, `{}`, `"tool_calls"`)
	} else {
		s.writeSSEChunk(w, `{"role":"assistant","content":"你好"}`, "null")
		s.writeSSEChunk(w, `{"content":"，世界"}`, "null")
		s.writeSSEChunk(w, `{}`, `"stop"`)
	}
	s.writeBody(w, "data: [DONE]\n\n")
}

func (s *openAIServer) writeSSEChunk(w http.ResponseWriter, delta, finishReason string) {
	s.writeBody(w, fmt.Sprintf("data: "+sseChunkTemplate+"\n\n", delta, finishReason))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *openAIServer) writeBody(w http.ResponseWriter, text string) {
	if _, err := io.WriteString(w, text); err != nil {
		s.t.Errorf("write SSE response: %v", err)
	}
}

func (s *openAIServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func (s *openAIServer) bodyAt(t *testing.T, index int) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Greater(t, len(s.bodies), index)
	return s.bodies[index]
}
