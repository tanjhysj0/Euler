package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"euler/internal/approval"
	"euler/internal/event"
	"euler/internal/workspace"
)

// unitRecorder 在内存中记录工具调用生命周期，替代 GORM 实现。
type unitRecorder struct {
	mu        sync.Mutex
	started   []Call
	succeeded map[uuid.UUID]string
	failed    map[uuid.UUID]string
	rejected  map[uuid.UUID]string
}

func newUnitRecorder() *unitRecorder {
	return &unitRecorder{
		succeeded: make(map[uuid.UUID]string),
		failed:    make(map[uuid.UUID]string),
		rejected:  make(map[uuid.UUID]string),
	}
}

func (r *unitRecorder) Start(_ context.Context, call Call) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, call)
	return nil
}

func (r *unitRecorder) Succeed(_ context.Context, id uuid.UUID, result string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.succeeded[id] = result
	return nil
}

func (r *unitRecorder) Fail(
	_ context.Context, id uuid.UUID, resultJSON, message string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed[id] = resultJSON + message
	return nil
}

func (r *unitRecorder) Reject(_ context.Context, id uuid.UUID, result string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejected[id] = result
	return nil
}

// recorderCounts 汇总一次测试中的记账结果。
type recorderCounts struct {
	started   int
	succeeded int
	failed    int
	rejected  int
}

func (r *unitRecorder) counts() recorderCounts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return recorderCounts{
		started:   len(r.started),
		succeeded: len(r.succeeded),
		failed:    len(r.failed),
		rejected:  len(r.rejected),
	}
}

// unitPublisher 记录事件类型，不触碰数据库。
type unitPublisher struct {
	mu    sync.Mutex
	types []string
}

func (p *unitPublisher) Append(
	_ context.Context, runID uuid.UUID, eventType string, _ any,
) (event.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.types = append(p.types, eventType)
	return event.Event{RunID: runID, Type: eventType}, nil
}

func (*unitPublisher) ListAfter(
	context.Context, uuid.UUID, int64,
) ([]event.Event, error) {
	return nil, nil
}

func (*unitPublisher) Subscribe(uuid.UUID) (<-chan event.Event, func()) {
	return nil, func() {}
}

func (p *unitPublisher) eventTypes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	types := make([]string, len(p.types))
	copy(types, p.types)
	return types
}

// unitFiles 是文件工具端口的纯内存实现。
type unitFiles struct {
	content map[string][]byte
}

func (f *unitFiles) ReadLines(
	_ context.Context, path string, offset, limit int,
) (workspace.LinePage, error) {
	data, ok := f.content[path]
	if !ok {
		return workspace.LinePage{}, workspace.ErrNotFound
	}
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = 2000
	}
	lines := strings.Split(string(data), "\n")
	if offset > len(lines) {
		return workspace.LinePage{StartLine: offset}, nil
	}
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	page := workspace.LinePage{
		Content:   strings.Join(lines[offset-1:end], "\n"),
		StartLine: offset,
		EndLine:   end,
		HasMore:   end < len(lines),
	}
	if page.HasMore {
		page.NextOffset = end + 1
	}
	return page, nil
}

func (*unitFiles) ListDir(context.Context, string, string, int) (workspace.NodePage, error) {
	return workspace.NodePage{}, nil
}

func (*unitFiles) Search(context.Context, string, string, int) (workspace.SearchPage, error) {
	return workspace.SearchPage{}, nil
}

func (f *unitFiles) PrepareWrite(
	_ context.Context, path string, content []byte,
) (workspace.FileChange, error) {
	return workspace.FileChange{
		Diff: "write " + path,
		Apply: func(context.Context) error {
			f.content[path] = content
			return nil
		},
	}, nil
}

func (*unitFiles) PreparePatch(
	context.Context, string, string, string,
) (workspace.FileChange, error) {
	return workspace.FileChange{}, errors.New("not implemented")
}

func (*unitFiles) Delete(context.Context, string) (workspace.FileChange, error) {
	return workspace.FileChange{}, errors.New("not implemented")
}

// unitExecutor 用固定结果替代宿主 shell。
type unitExecutor struct {
	mu       sync.Mutex
	requests []ShellRequest
	result   ShellResult
}

func (e *unitExecutor) Execute(_ context.Context, request ShellRequest) ShellResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, request)
	return e.result
}

func (e *unitExecutor) calls() []ShellRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	requests := make([]ShellRequest, len(e.requests))
	copy(requests, e.requests)
	return requests
}

func newUnitRegistry(
	recorder CallRecorder, events event.Publisher, gate approval.Gate, maxCalls int,
) *Registry {
	return NewRegistry(RegistryDeps{
		Calls: recorder, Events: events, Gate: gate, RunID: uuid.New(),
		MaxToolCalls: maxCalls, MaxOutputBytes: 4096,
	})
}

func invokeUnitTool(
	t *testing.T, built []tool.BaseTool, name, args string,
) (string, error) {
	t.Helper()
	for _, candidate := range built {
		info, err := candidate.Info(context.Background())
		require.NoError(t, err)
		if info.Name != name {
			continue
		}
		invokable, ok := candidate.(tool.InvokableTool)
		require.True(t, ok)
		return invokable.InvokableRun(context.Background(), args)
	}
	t.Fatalf("tool %s not registered", name)
	return "", nil
}

func TestRegistryReadsFileWithoutDatabase(t *testing.T) {
	recorder := newUnitRecorder()
	events := &unitPublisher{}
	registry := newUnitRegistry(recorder, events, &fakeGate{
		decision: approval.Decision{Approved: true, Status: approval.StatusApproved},
	}, 10)
	built, err := registry.Tools(
		&unitFiles{content: map[string][]byte{"a.txt": []byte("你好")}},
		&unitExecutor{}, "/tmp/unit", nil,
	)
	require.NoError(t, err)
	observation, err := invokeUnitTool(t, built, "file_read", `{"path":"a.txt"}`)
	require.NoError(t, err)
	require.Contains(t, observation, "你好")
	counts := recorder.counts()
	require.Equal(t, 1, counts.started)
	require.Equal(t, 1, counts.succeeded)
	require.Equal(t, []string{"tool.started", "tool.completed"}, events.eventTypes())
}

func TestRegistryReadPagesWithOffsetAndLimit(t *testing.T) {
	recorder := newUnitRecorder()
	events := &unitPublisher{}
	registry := newUnitRegistry(recorder, events, &fakeGate{
		decision: approval.Decision{Approved: true, Status: approval.StatusApproved},
	}, 10)
	content := "l1\nl2\nl3\nl4\nl5"
	built, err := registry.Tools(
		&unitFiles{content: map[string][]byte{"a.txt": []byte(content)}},
		&unitExecutor{}, "/tmp/unit", nil,
	)
	require.NoError(t, err)
	observation, err := invokeUnitTool(t, built, "file_read",
		`{"path":"a.txt","offset":2,"limit":2}`)
	require.NoError(t, err)
	require.Contains(t, observation, "l2\\nl3")
	require.Contains(t, observation, `"start_line":2`)
	require.Contains(t, observation, `"end_line":3`)
	require.Contains(t, observation, `"has_more":true`)
	require.Contains(t, observation, `"next_offset":4`)
}

func TestRegistryRejectsSensitiveToolWithoutExecuting(t *testing.T) {
	recorder := newUnitRecorder()
	events := &unitPublisher{}
	executor := &unitExecutor{}
	registry := newUnitRegistry(recorder, events, &fakeGate{
		decision: approval.Decision{Approved: false, Status: approval.StatusRejected},
	}, 10)
	built, err := registry.Tools(&unitFiles{}, executor, "/tmp/unit", nil)
	require.NoError(t, err)
	observation, err := invokeUnitTool(t, built, "shell", `{"command":"rm -rf /"}`)
	require.NoError(t, err)
	require.Contains(t, observation, "rejected")
	require.Empty(t, executor.calls())
	counts := recorder.counts()
	require.Equal(t, 1, counts.started)
	require.Equal(t, 1, counts.rejected)
}

func TestRegistryApprovedShellUsesInjectedExecutor(t *testing.T) {
	recorder := newUnitRecorder()
	executor := &unitExecutor{result: ShellResult{Output: "ok"}}
	registry := newUnitRegistry(recorder, &unitPublisher{}, &fakeGate{
		decision: approval.Decision{Approved: true, Status: approval.StatusApproved},
	}, 10)
	built, err := registry.Tools(&unitFiles{}, executor, "/tmp/unit", nil)
	require.NoError(t, err)
	observation, err := invokeUnitTool(t, built, "shell", `{"command":"echo ok"}`)
	require.NoError(t, err)
	require.Contains(t, observation, "ok")
	require.Len(t, executor.calls(), 1)
	counts := recorder.counts()
	require.Equal(t, 1, counts.succeeded)
}

func TestRegistryEnforcesBudgetWithoutDatabase(t *testing.T) {
	recorder := newUnitRecorder()
	registry := newUnitRegistry(recorder, &unitPublisher{}, &fakeGate{
		decision: approval.Decision{Approved: true, Status: approval.StatusApproved},
	}, 1)
	built, err := registry.Tools(
		&unitFiles{content: map[string][]byte{"a.txt": []byte("x")}},
		&unitExecutor{}, "/tmp/unit", nil,
	)
	require.NoError(t, err)
	_, err = invokeUnitTool(t, built, "file_read", `{"path":"a.txt"}`)
	require.NoError(t, err)
	_, err = invokeUnitTool(t, built, "file_read", `{"path":"a.txt"}`)
	require.ErrorIs(t, err, ErrToolCallLimit)
	counts := recorder.counts()
	require.Equal(t, 2, counts.started)
	require.Equal(t, 1, counts.failed)
}

// 保证 fake 在编译期满足端口。
var (
	_ CallRecorder    = (*unitRecorder)(nil)
	_ event.Publisher = (*unitPublisher)(nil)
	_ FileStore       = (*unitFiles)(nil)
	_ Executor        = (*unitExecutor)(nil)
)

func TestRegistryAllowsUnlimitedToolCalls(t *testing.T) {
	recorder := newUnitRecorder()
	registry := newUnitRegistry(recorder, &unitPublisher{}, &fakeGate{
		decision: approval.Decision{Approved: true, Status: approval.StatusApproved},
	}, 0)
	built, err := registry.Tools(
		&unitFiles{content: map[string][]byte{"a.txt": []byte("x")}},
		&unitExecutor{}, "/tmp/unit", nil,
	)
	require.NoError(t, err)
	for range 3 {
		_, err = invokeUnitTool(t, built, "file_read", `{"path":"a.txt"}`)
		require.NoError(t, err)
	}
	counts := recorder.counts()
	require.Equal(t, 3, counts.started)
	require.Zero(t, counts.failed)
}

// TestRegistrySubmitReportsCompletion 确认 submit 无需审批即可记录验收摘要，
// 并由运行时的直接返回分支负责结束。
func TestRegistrySubmitReportsCompletion(t *testing.T) {
	recorder := newUnitRecorder()
	registry := newUnitRegistry(recorder, &unitPublisher{}, &fakeGate{}, 10)
	built, err := registry.Tools(&unitFiles{}, &unitExecutor{}, "/tmp/unit", nil)
	require.NoError(t, err)
	observation, err := invokeUnitTool(t, built, "submit", `{"summary":"验收点全部通过"}`)
	require.NoError(t, err)
	require.Contains(t, observation, "验收点全部通过")
	counts := recorder.counts()
	require.Equal(t, 1, counts.started)
	require.Equal(t, 1, counts.succeeded)
}
