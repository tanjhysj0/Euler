package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"euler/eval/internal/task"
	"euler/eval/internal/workspace"
)

func TestDiffStats(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@\n-old\n+new\ndiff --git a/x_test.go b/x_test.go\n+++ b/x_test.go\n"
	files, added, deleted, touched := DiffStats(patch)
	if files != 2 || added != 1 || deleted != 1 || !touched {
		t.Fatalf("stats = %d %d %d %v", files, added, deleted, touched)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}

// newRepo 构造一个只有 a.txt 的评测工作区。
func newRepo(t *testing.T) *workspace.Repo {
	t.Helper()
	fixture := t.TempDir()
	mustWrite(t, filepath.Join(fixture, "a.txt"), "v1\n")
	repo, err := workspace.Materialize(fixture, filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatalf("物化: %v", err)
	}
	return repo
}

func TestRunTaskAutoApprovesAndExtractsPatch(t *testing.T) {
	repo := newRepo(t)
	var approved atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "euler_local", Value: "t", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/model-profiles", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"p1","model":"m1"}`)
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"s1"}`)
	})
	mux.HandleFunc("/api/v1/sessions/s1/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"run":{"id":"r1"}}`)
	})
	mux.HandleFunc("/api/v1/runs/r1/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"seq\":1,\"type\":\"approval.requested\","+
			"\"payload\":{\"approval_id\":\"a1\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"seq\":2,\"type\":\"run.completed\",\"payload\":{}}\n\n")
	})
	mux.HandleFunc("/api/v1/approvals/", func(w http.ResponseWriter, _ *http.Request) {
		approved.Add(1)
		_, _ = fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("/api/v1/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"run":{"status":"succeeded"},"tool_calls":[{}],"approvals":[{}]}`)
	})
	mux.HandleFunc("/api/v1/sessions/s1/messages", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"messages":[{"id":"m1","role":"assistant","content":"ok"}],"next_after":0}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	transcripts := t.TempDir()
	client, err := NewClient(context.Background(), Options{
		BaseURL: server.URL, ModelBaseURL: "http://example", Model: "m",
		APIKeyEnv: "TEST_KEY", RunTimeout: 5 * time.Second,
		TranscriptDir: transcripts,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	mustWrite(t, filepath.Join(repo.Dir, "a.txt"), "v2\n")
	pred := client.RunTask(context.Background(),
		task.Task{ID: "demo-001", Issue: "修复"}, repo)
	if pred.Meta.Status != statusCompleted {
		t.Fatalf("status = %s, err = %s", pred.Meta.Status, pred.Meta.Error)
	}
	if approved.Load() != 1 {
		t.Fatalf("审批次数 = %d", approved.Load())
	}
	if !strings.Contains(pred.ModelPatch, "v2") {
		t.Fatalf("补丁未包含改动:\n%s", pred.ModelPatch)
	}
	if pred.Meta.Approvals != 1 || pred.Meta.ToolCalls != 1 {
		t.Fatalf("meta = %+v", pred.Meta)
	}
	if pred.Meta.DurationMS <= 0 {
		t.Fatalf("duration_ms = %d, want > 0", pred.Meta.DurationMS)
	}
	transcript, readErr := os.ReadFile(filepath.Join(transcripts, "demo-001.json"))
	if readErr != nil {
		t.Fatalf("读取轨迹: %v", readErr)
	}
	if !strings.Contains(string(transcript), `"m1"`) {
		t.Fatalf("轨迹缺少消息: %s", transcript)
	}
}

func TestRunTaskMarksNoActionWhenRunDoesNothing(t *testing.T) {
	repo := newRepo(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "euler_local", Value: "t", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/model-profiles", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"p1","model":"m1"}`)
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"s1"}`)
	})
	mux.HandleFunc("/api/v1/sessions/s1/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"run":{"id":"r1"}}`)
	})
	mux.HandleFunc("/api/v1/runs/r1/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"seq\":1,\"type\":\"run.completed\",\"payload\":{}}\n\n")
	})
	mux.HandleFunc("/api/v1/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"run":{"status":"succeeded"},"tool_calls":[],"approvals":[]}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewClient(context.Background(), Options{
		BaseURL: server.URL, ModelBaseURL: "http://example", Model: "m",
		APIKeyEnv: "TEST_KEY", RunTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	pred := client.RunTask(context.Background(),
		task.Task{ID: "demo-001", Issue: "修复"}, repo)
	if pred.Meta.Status != statusNoAction {
		t.Fatalf("status = %s, err = %s", pred.Meta.Status, pred.Meta.Error)
	}
	if pred.ModelPatch != "" {
		t.Fatalf("空跑不应产生补丁: %q", pred.ModelPatch)
	}
}

func TestPollApprovesPendingApproval(t *testing.T) {
	var approved atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"run":{"status":"succeeded"},"tool_calls":[],"approvals":[{"id":"a1","status":"pending"}]}`)
	})
	mux.HandleFunc("/api/v1/approvals/a1/decision", func(w http.ResponseWriter, _ *http.Request) {
		approved.Add(1)
		_, _ = fmt.Fprint(w, `{}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		jsonClient: server.Client(),
		sseClient:  server.Client(),
	}
	var metrics runMetrics
	terminal, err := client.poll(context.Background(), "r1", &metrics)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if terminal != "run.completed" {
		t.Fatalf("terminal = %s, want run.completed", terminal)
	}
	if approved.Load() != 1 {
		t.Fatalf("审批次数 = %d, want 1", approved.Load())
	}
}

func TestPollMapsInterrupted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"run":{"status":"interrupted"},"tool_calls":[],"approvals":[]}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := &Client{
		baseURL:    server.URL,
		jsonClient: server.Client(),
		sseClient:  server.Client(),
	}
	var metrics runMetrics
	terminal, err := client.poll(context.Background(), "r1", &metrics)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if terminal != "run.interrupted" {
		t.Fatalf("terminal = %s, want run.interrupted", terminal)
	}
}

func TestRunTaskTimeoutCancels(t *testing.T) {
	repo := newRepo(t)
	var cancelled atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/model-profiles", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"p1","model":"m1"}`)
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"s1"}`)
	})
	mux.HandleFunc("/api/v1/sessions/s1/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"run":{"id":"r1"}}`)
	})
	mux.HandleFunc("/api/v1/runs/r1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		<-r.Context().Done()
	})
	mux.HandleFunc("/api/v1/runs/r1/cancel", func(w http.ResponseWriter, _ *http.Request) {
		cancelled.Add(1)
		_, _ = fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("/api/v1/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"run":{"status":"running"},"tool_calls":[],"approvals":[]}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewClient(context.Background(), Options{
		BaseURL: server.URL, ModelBaseURL: "http://example", Model: "m",
		APIKeyEnv: "TEST_KEY", RunTimeout: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	pred := client.RunTask(context.Background(),
		task.Task{ID: "demo-001", Issue: "修复"}, repo)
	if pred.Meta.Status != statusTimeout {
		t.Fatalf("status = %s, want timeout", pred.Meta.Status)
	}
	if cancelled.Load() != 1 {
		t.Fatalf("cancel 调用次数 = %d", cancelled.Load())
	}
}
