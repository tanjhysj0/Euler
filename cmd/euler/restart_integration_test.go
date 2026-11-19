//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"euler/internal/persistence"
	"euler/internal/run"
	"euler/internal/session"
)

const (
	restartTestEnvKey   = "EULER_TEST_MODEL_KEY"
	restartTestEnvValue = "euler-not-a-real-secret"
	restartPollTimeout  = 20 * time.Second
	restartPollInterval = 50 * time.Millisecond
	restartHTTPTimeout  = 10 * time.Second
	restartChunkFmt     = "data: {\"id\":\"chatcmpl-restart\"," +
		"\"object\":\"chat.completion.chunk\"," +
		"\"created\":1700000000,\"model\":\"euler-restart-mock\"," +
		"\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]}\n\n"
)

func TestProcessRestartMarksActiveRunInterrupted(t *testing.T) {
	fixture := newEulerProcessFixture(t)
	runID := fixture.startRunAwaitingFileApproval(t)
	fixture.killWithSIGKILL(t)
	fixture.restart(t)

	snapshot := fixture.runSnapshot(t, runID)
	require.Equal(t, "interrupted", snapshot.Run.Status)
	require.Equal(t, "interrupted", fixture.assistantMessage(t, runID).Status)
	require.Len(t, snapshot.ToolCalls, 1)
	require.Equal(t, "cancelled", snapshot.ToolCalls[0].Status)
	require.Len(t, snapshot.Approvals, 1)
	require.Equal(t, "cancelled", snapshot.Approvals[0].Status)
	require.NoFileExists(t, fixture.workspaceFile("note.txt"))
	require.Zero(t, fixture.modelRequestsSinceRestart())

	continuedRunID := fixture.startRun(t, "请基于完整历史显式继续")
	require.NotEqual(t, runID, continuedRunID)
}

// restartMock 是进程内 OpenAI 兼容 SSE 服务，记录请求数以便断言重启后不自动续跑。
type restartMock struct {
	mu       sync.Mutex
	requests int
	server   *httptest.Server
}

func newRestartMock(t *testing.T) *restartMock {
	t.Helper()
	mock := &restartMock{}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *restartMock) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.requests++
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	if strings.Contains(string(body), `"role":"tool"`) {
		if !writeSSE(w, restartChunk(`{"role":"assistant","content":"已收到工具结果。"}`, "null")) {
			return
		}
		if !writeSSE(w, restartChunk(`{}`, `"stop"`)) {
			return
		}
	} else if !writeRestartToolCall(w) {
		return
	}
	writeSSE(w, "data: [DONE]\n\n")
}

func (m *restartMock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

func restartChunk(delta, finish string) string {
	return fmt.Sprintf(restartChunkFmt, delta, finish)
}

// writeSSE 写入一帧 SSE；客户端断流时返回 false，调用方停止后续写入。
func writeSSE(w http.ResponseWriter, frame string) bool {
	_, err := fmt.Fprint(w, frame)
	return err == nil
}

func writeRestartToolCall(w http.ResponseWriter) bool {
	return writeSSE(w, restartChunk(`{"role":"assistant"}`, "null")) &&
		writeSSE(w, restartChunk(`{"tool_calls":[{"index":0,"id":"call_restart_1",`+
			`"type":"function","function":{"name":"file_write",`+
			`"arguments":"{\"path\":\"note.txt\",\"content\":\"# 重启验收\"}"}}]}`, "null")) &&
		writeSSE(w, restartChunk(`{}`, `"tool_calls"`))
}

// eulerProcessFixture 驱动真实编译出的 euler 二进制：临时目录、独立端口、
// 独立工作区与测试数据库，绝不触碰开发库或用户默认工作区。
type eulerProcessFixture struct {
	dir           string
	databaseURL   string
	baseURL       string
	workspacePath string
	client        *http.Client
	cmd           *exec.Cmd
	mock          *restartMock
	db            *gorm.DB
	profileID     uuid.UUID
	sessionID     uuid.UUID
	workspaceID   uuid.UUID
	baseline      int
}

func newEulerProcessFixture(t *testing.T) *eulerProcessFixture {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	fixture := &eulerProcessFixture{
		dir:         t.TempDir(),
		databaseURL: databaseURL,
		mock:        newRestartMock(t),
		client:      &http.Client{Jar: jar, Timeout: restartHTTPTimeout},
	}
	fixture.buildBinary(t, repoRoot)
	fixture.writeConfig(t)
	fixture.openDatabase(t)
	fixture.startProcess(t)
	fixture.bootstrap(t)
	fixture.createProfileAndSession(t)
	return fixture
}

// buildBinary 用 go install 把真实二进制装进临时目录。
// exec 参数全部为常量以满足 gosec G204，输出目录经 GOBIN 环境变量传入。
// 子进程 cwd 为临时目录，符号链接仓库 migrations 保证启动迁移可解析。
func (f *eulerProcessFixture) buildBinary(t *testing.T, repoRoot string) {
	t.Helper()
	cmd := exec.Command("go", "install", "./cmd/euler")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOBIN="+f.dir)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.NoError(t, os.Symlink(
		filepath.Join(repoRoot, "migrations"), filepath.Join(f.dir, "migrations")))
}

func (f *eulerProcessFixture) writeConfig(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.NoError(t, listener.Close())
	f.baseURL = fmt.Sprintf("http://127.0.0.1:%d", addr.Port)
	content := fmt.Sprintf("listen: 127.0.0.1:%d\nworkspace_root: %s\nallowed_origins:\n  - %s\n",
		addr.Port, filepath.Join(f.dir, "workspaces"), f.baseURL)
	require.NoError(t, os.WriteFile(filepath.Join(f.dir, "config.yaml"), []byte(content), 0o600))
}

func (f *eulerProcessFixture) openDatabase(t *testing.T) {
	t.Helper()
	db, err := persistence.Open(context.Background(), f.databaseURL)
	require.NoError(t, err)
	f.db = db
	t.Cleanup(func() { f.cleanupDatabase(t) })
}

// startProcess 启动子进程；exec 参数全部为常量（gosec G204），
// 二进制与配置文件都在临时目录内，经 cmd.Dir 定位。
func (f *eulerProcessFixture) startProcess(t *testing.T) {
	t.Helper()
	cmd := exec.Command("./euler", "-config", "config.yaml")
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(),
		"EULER_DATABASE_URL="+f.databaseURL,
		restartTestEnvKey+"="+restartTestEnvValue,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	f.cmd = cmd
	t.Cleanup(func() { f.killProcess(t) })
	require.Eventually(t, f.healthy, restartPollTimeout, restartPollInterval)
}

func (f *eulerProcessFixture) healthy() bool {
	resp, err := f.client.Get(f.baseURL + "/health")
	if err != nil {
		return false
	}
	closeErr := resp.Body.Close()
	return resp.StatusCode == http.StatusOK && closeErr == nil
}

// bootstrap 种下每进程内存 token 的会话 cookie；重启后必须重新执行。
func (f *eulerProcessFixture) bootstrap(t *testing.T) {
	t.Helper()
	var out map[string]any
	f.getJSON(t, "/api/v1/bootstrap", &out)
}

func (f *eulerProcessFixture) createProfileAndSession(t *testing.T) {
	t.Helper()
	var profile struct {
		ID uuid.UUID `json:"id"`
	}
	f.postJSON(t, "/api/v1/model-profiles", map[string]any{
		"name":        "重启验收模型",
		"base_url":    f.mock.server.URL + "/v1",
		"model":       "euler-restart-mock",
		"secret_kind": "environment",
		"secret_ref":  restartTestEnvKey,
	}, &profile)
	f.profileID = profile.ID
	var created session.CreateResult
	f.postJSON(t, "/api/v1/sessions", map[string]any{
		"profile_id": profile.ID,
		"path":       "",
	}, &created)
	f.sessionID = created.ID
	f.workspaceID = created.Workspace.ID
	f.workspacePath = created.Workspace.Path
}

func (f *eulerProcessFixture) startRun(t *testing.T, content string) uuid.UUID {
	t.Helper()
	var started run.StartResult
	f.postJSON(t, "/api/v1/sessions/"+f.sessionID.String()+"/runs",
		map[string]any{"content": content}, &started)
	return started.Run.ID
}

func (f *eulerProcessFixture) startRunAwaitingFileApproval(t *testing.T) uuid.UUID {
	t.Helper()
	runID := f.startRun(t, "请创建 note.txt 验收文件")
	require.Eventually(t, func() bool {
		snapshot, err := f.fetchSnapshot(runID)
		return err == nil && snapshot.Run.Status == "waiting_approval"
	}, restartPollTimeout, restartPollInterval)
	return runID
}

func (f *eulerProcessFixture) killWithSIGKILL(t *testing.T) {
	t.Helper()
	require.NoError(t, f.cmd.Process.Kill())
	_, err := f.cmd.Process.Wait()
	require.NoError(t, err)
}

// killProcess 容忍已被测试体或另一条 cleanup 杀掉并回收的进程：
// Process.Wait 不会设置 cmd.ProcessState，不能据此判断是否已退出。
func (f *eulerProcessFixture) killProcess(t *testing.T) {
	t.Helper()
	if f.cmd == nil {
		return
	}
	if err := f.cmd.Process.Kill(); err != nil {
		require.ErrorIs(t, err, os.ErrProcessDone)
		return
	}
	_, err := f.cmd.Process.Wait()
	require.NoError(t, err)
}

func (f *eulerProcessFixture) restart(t *testing.T) {
	t.Helper()
	f.baseline = f.mock.count()
	f.startProcess(t)
	f.bootstrap(t)
}

func (f *eulerProcessFixture) runSnapshot(t *testing.T, runID uuid.UUID) run.Snapshot {
	t.Helper()
	snapshot, err := f.fetchSnapshot(runID)
	require.NoError(t, err)
	return snapshot
}

// fetchSnapshot 不使用 require，可在 require.Eventually 的轮询 goroutine 中安全调用。
func (f *eulerProcessFixture) fetchSnapshot(runID uuid.UUID) (run.Snapshot, error) {
	var snapshot run.Snapshot
	resp, err := f.client.Get(f.baseURL + "/api/v1/runs/" + runID.String())
	if err != nil {
		return snapshot, fmt.Errorf("get run: %w", err)
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&snapshot)
	closeErr := resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snapshot, fmt.Errorf("get run: status %d", resp.StatusCode)
	}
	if decodeErr != nil {
		return snapshot, fmt.Errorf("decode run: %w", decodeErr)
	}
	return snapshot, closeErr
}

func (f *eulerProcessFixture) assistantMessage(t *testing.T, runID uuid.UUID) session.MessageView {
	t.Helper()
	var page session.MessagePage
	f.getJSON(t, "/api/v1/sessions/"+f.sessionID.String()+"/messages?limit=200", &page)
	for _, message := range page.Messages {
		if message.Role == "assistant" && message.RunID != nil && *message.RunID == runID {
			return message
		}
	}
	t.Fatalf("assistant message for run %s not found", runID)
	return session.MessageView{}
}

func (f *eulerProcessFixture) workspaceFile(name string) string {
	return filepath.Join(f.workspacePath, name)
}

func (f *eulerProcessFixture) modelRequestsSinceRestart() int {
	return f.mock.count() - f.baseline
}

func (f *eulerProcessFixture) getJSON(t *testing.T, path string, out any) {
	t.Helper()
	resp, err := f.client.Get(f.baseURL + path)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Contains(t, []int{http.StatusOK, http.StatusNoContent}, resp.StatusCode)
	if resp.StatusCode == http.StatusNoContent {
		return
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
}

func (f *eulerProcessFixture) postJSON(t *testing.T, path string, body any, out any) {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := f.client.Post(f.baseURL+path, "application/json", bytes.NewReader(payload))
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Contains(t,
		[]int{http.StatusOK, http.StatusCreated, http.StatusAccepted}, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
}

func (f *eulerProcessFixture) cleanupDatabase(t *testing.T) {
	t.Helper()
	if f.sessionID != uuid.Nil {
		f.deleteOwnRows(t)
	}
	sqlDB, err := f.db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

// deleteOwnRows 按外键顺序只删除本测试自建的行。
// tool_calls.approval_id 与 approvals.tool_call_id 互为外键，先置空打破循环。
func (f *eulerProcessFixture) deleteOwnRows(t *testing.T) {
	t.Helper()
	require.NoError(t, f.db.Exec(
		"UPDATE tool_calls SET approval_id = NULL "+
			"WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
		f.sessionID).Error)
	queries := []string{
		"DELETE FROM approvals WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
		"DELETE FROM tool_calls WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
		"DELETE FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
		"DELETE FROM messages WHERE session_id = ?",
		"DELETE FROM runs WHERE session_id = ?",
	}
	for _, query := range queries {
		require.NoError(t, f.db.Exec(query, f.sessionID).Error)
	}
	require.NoError(t, f.db.Exec("DELETE FROM sessions WHERE id = ?", f.sessionID).Error)
	require.NoError(t, f.db.Exec("DELETE FROM workspaces WHERE id = ?", f.workspaceID).Error)
	require.NoError(t, f.db.Exec("DELETE FROM model_profiles WHERE id = ?", f.profileID).Error)
}
