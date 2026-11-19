# euleval 编码能力评测系统实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付一个独立 Go module `eval/`，用黑盒 HTTP 驱动 Euler Agent 在隔离 git 工作区修 Bug，并以 FAIL_TO_PASS / PASS_TO_PASS 硬判分。

**Architecture:** Runner 与 Judge 严格分离。Runner 物化 buggy 基线、驱动真实 Euler 服务、自动批准工具审批、提取 `model_patch` 写入 `preds.jsonl`；Judge 从 fixture 重新物化干净基线，只应用补丁文本与隐藏测试，跑 `go test -json` 判定。`smoke` 用 gold 补丁走 Judge，不需要服务与模型。

**Tech Stack:** Go 1.26.5、标准库（net/http、encoding/json、os/exec）、git CLI、`go test -json`、golangci-lint v2。

**Spec:** [2026-11-19-euler-coding-eval-design.md](file:///Users/jason/go/src/Euler/docs/superpowers/specs/2026-11-19-euler-coding-eval-design.md)

## Global Constraints

- Go 版本：`go 1.26.5`（与主模块一致）。
- `eval/` 是独立 module `euler/eval`，**只允许标准库**，不得 import `euler/internal/...`。
- 所有代码注释使用中文；导出符号必须有中文 godoc。
- `eval/` 与 `eval/fixture/` 都是 module 根，各自必须有 `.golangci.yml`。
- 每次改完代码必须执行 `cd eval && golangci-lint run --timeout=5m ./...`，零 issue。
- 单测不得依赖 PostgreSQL、Euler 服务或模型 Key。
- 评测产物（`results/`、临时工作区）不得进入 git；`eval/.gitignore` 必须包含 `results/`。
- 提交动作仅在用户明确要求时执行；每个任务末尾的检查点只运行验证命令并汇报结果。
- 补丁一律用 `git apply` 应用，失败必须显式报错，禁止静默跳过。
- 隐藏测试与 gold 补丁永远不得复制进 Runner 工作区。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `eval/cmd/euleval/main.go` | CLI 分发：`tasks` / `run` / `judge` / `smoke` |
| `eval/internal/task/task.go` | 题目加载、默认值、基础校验、过滤 |
| `eval/internal/task/validate.go` | 补丁可应用性校验 |
| `eval/internal/workspace/workspace.go` | fixture 复制、git init、补丁应用、diff 提取、测试还原 |
| `eval/internal/workspace/git.go` | git 子进程封装 |
| `eval/internal/report/report.go` | preds.jsonl、report.json、控制台摘要 |
| `eval/internal/judge/judge.go` | 判分流水线 |
| `eval/internal/judge/gotest.go` | `go test -json` 解析与判定 |
| `eval/internal/runner/runner.go` | Euler HTTP 客户端与单题编排 |
| `eval/internal/runner/events.go` | SSE 消费、自动审批、重连/轮询 |
| `eval/fixture/` | 4 个纯标准库包与可见测试 |
| `eval/tasks/<id>/` | 5 道题（task.json + 3 个补丁） |

---

### Task 1: eval module 骨架与题目加载器

**Files:**
- Create: `eval/go.mod`
- Create: `eval/.gitignore`
- Create: `eval/internal/task/task.go`
- Test: `eval/internal/task/task_test.go`

**Interfaces:**
- Produces:
  - `task.Task` 结构体（字段见下）
  - `task.Load(root string) ([]Task, error)`
  - `task.Filter(tasks []Task, ids []string) ([]Task, error)`
  - `task.Task.ValidateBasic() error`
  - `task.Task.BugPatch() string`、`TestPatch() string`、`GoldPatch() string`
  - `task.Task.WithDefaults() Task`

- [ ] **Step 1: 创建 module 骨架**

`eval/go.mod`：

```go
module euler/eval

go 1.26.5
```

`eval/.gitignore`：

```
results/
*.tmp
```

- [ ] **Step 2: 写失败测试**

`eval/internal/task/task_test.go`：

```go
package task

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTask(t *testing.T, root, id, body string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建题目目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("写 task.json: %v", err)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "demo-001", `{
		"id": "demo-001",
		"title": "示例",
		"issue": "修复示例",
		"fail_to_pass": ["TestA"],
		"pass_to_pass": ["TestB"]
	}`)
	tasks, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("题目数 = %d", len(tasks))
	}
	if tasks[0].TestCommand != defaultTestCommand {
		t.Fatalf("默认 test_command = %q", tasks[0].TestCommand)
	}
	if tasks[0].TimeoutSeconds != defaultTimeoutSeconds {
		t.Fatalf("默认 timeout = %d", tasks[0].TimeoutSeconds)
	}
}

func TestValidateBasicRejectsOverlap(t *testing.T) {
	task := Task{
		ID: "demo-001", Issue: "x",
		FailToPass: []string{"TestA"},
		PassToPass: []string{"TestA"},
	}
	task.Dir = filepath.Join("tasks", "demo-001")
	if err := task.WithDefaults().ValidateBasic(); err == nil {
		t.Fatal("F2P 与 P2P 重叠必须报错")
	}
}

func TestFilterReportsMissingID(t *testing.T) {
	_, err := Filter([]Task{{ID: "a"}}, []string{"a", "b"})
	if err == nil {
		t.Fatal("缺失题目 ID 必须报错")
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `cd eval && go test ./internal/task/ -run TestLoad -v`
Expected: FAIL，`undefined: Load`

- [ ] **Step 4: 实现 task.go**

`eval/internal/task/task.go`：

```go
// Package task 负责加载与校验 euleval 题库。
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultTestCommand    = "go test -count=1 -json ./..."
	defaultTimeoutSeconds = 900
	minimumTimeoutSeconds = 60
	taskFileName          = "task.json"
)

// ErrInvalid 表示题目定义不合法。
var ErrInvalid = errors.New("invalid task")

// Task 是一道编码评测题。
type Task struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Issue          string   `json:"issue"`
	Difficulty     string   `json:"difficulty,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	ProtectedTests []string `json:"protected_tests,omitempty"`
	FailToPass     []string `json:"fail_to_pass"`
	PassToPass     []string `json:"pass_to_pass"`
	TestCommand    string   `json:"test_command,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`

	// Dir 是题目目录的绝对路径，不参与 JSON 序列化。
	Dir string `json:"-"`
}

// Load 读取 root 下所有子目录中的 task.json。
func Load(root string) ([]Task, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("读取题库目录: %w", err)
	}
	tasks := make([]Task, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		task, err := loadOne(dir)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task.WithDefaults())
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

func loadOne(dir string) (Task, error) {
	data, err := os.ReadFile(filepath.Join(dir, taskFileName))
	if err != nil {
		return Task{}, fmt.Errorf("读取 %s: %w", dir, err)
	}
	var value Task
	if err = json.Unmarshal(data, &value); err != nil {
		return Task{}, fmt.Errorf("%w: %s: %v", ErrInvalid, dir, err)
	}
	value.Dir = dir
	return value, nil
}

// WithDefaults 填充 test_command 与 timeout_seconds 的默认值。
func (t Task) WithDefaults() Task {
	if t.TestCommand == "" {
		t.TestCommand = defaultTestCommand
	}
	if t.TimeoutSeconds <= 0 {
		t.TimeoutSeconds = defaultTimeoutSeconds
	}
	return t
}

// BugPatch 返回注入 Bug 的补丁路径。
func (t Task) BugPatch() string { return filepath.Join(t.Dir, "bug.patch") }

// TestPatch 返回隐藏测试补丁路径。
func (t Task) TestPatch() string { return filepath.Join(t.Dir, "test.patch") }

// GoldPatch 返回参考修复补丁路径。
func (t Task) GoldPatch() string { return filepath.Join(t.Dir, "gold.patch") }

// ValidateBasic 校验不依赖 fixture 的字段约束。
func (t Task) ValidateBasic() error {
	if t.ID == "" || strings.ContainsAny(t.ID, `/\`) {
		return fmt.Errorf("%w: id 非法: %q", ErrInvalid, t.ID)
	}
	if filepath.Base(t.Dir) != t.ID {
		return fmt.Errorf("%w: 目录名与 id 不一致: %s", ErrInvalid, t.Dir)
	}
	if strings.TrimSpace(t.Issue) == "" {
		return fmt.Errorf("%w: %s: issue 为空", ErrInvalid, t.ID)
	}
	if len(t.FailToPass) == 0 || len(t.PassToPass) == 0 {
		return fmt.Errorf("%w: %s: fail_to_pass/pass_to_pass 不能为空", ErrInvalid, t.ID)
	}
	seen := make(map[string]struct{}, len(t.FailToPass))
	for _, name := range t.FailToPass {
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%w: %s: fail_to_pass 重复 %s", ErrInvalid, t.ID, name)
		}
		seen[name] = struct{}{}
	}
	for _, name := range t.PassToPass {
		if _, ok := seen[name]; ok {
			return fmt.Errorf("%w: %s: %s 同时出现在 F2P 与 P2P", ErrInvalid, t.ID, name)
		}
	}
	if !strings.Contains(t.TestCommand, "-json") {
		return fmt.Errorf("%w: %s: test_command 必须包含 -json", ErrInvalid, t.ID)
	}
	if t.TimeoutSeconds < minimumTimeoutSeconds {
		return fmt.Errorf("%w: %s: timeout_seconds 过小", ErrInvalid, t.ID)
	}
	return nil
}

// Filter 按 id 过滤；ids 为空时返回全部。
func Filter(tasks []Task, ids []string) ([]Task, error) {
	if len(ids) == 0 {
		return tasks, nil
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	result := make([]Task, 0, len(ids))
	for _, item := range tasks {
		if _, ok := want[item.ID]; ok {
			result = append(result, item)
			delete(want, item.ID)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: 题目不存在: %s", ErrInvalid, strings.Join(missing, ","))
	}
	return result, nil
}
```

- [ ] **Step 5: 运行测试**

Run: `cd eval && go test ./internal/task/ -v`
Expected: PASS（3 个测试）

- [ ] **Step 6: 检查点**

Run: `cd eval && gofmt -l . && git status --short`
Expected: gofmt 无输出；新增文件符合预期，不自动提交。

---

### Task 2: workspace 物化与补丁提取

**Files:**
- Create: `eval/internal/workspace/git.go`
- Create: `eval/internal/workspace/workspace.go`
- Test: `eval/internal/workspace/workspace_test.go`

**Interfaces:**
- Produces:
  - `workspace.Materialize(fixtureDir, dstDir string, patches ...string) (*Repo, error)`
  - `workspace.Repo{Dir, BaselineSHA string}`
  - `(*Repo) ApplyFile(path string) error`（`--3way`，不提交）
  - `(*Repo) ExtractPatch() (string, error)`
  - `(*Repo) Restore(path string) error`
  - `(*Repo) RemoveNewTestFiles() ([]string, error)`
  - `workspace.CopyDir(src, dst string) error`

- [ ] **Step 1: 写失败测试**

`eval/internal/workspace/workspace_test.go`：

```go
package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("写 fixture: %v", err)
	}
	return dir
}

func TestMaterializeAndExtractPatch(t *testing.T) {
	fixture := newFixture(t)
	repo, err := Materialize(fixture, filepath.Join(t.TempDir(), "ws1"))
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if repo.BaselineSHA == "" {
		t.Fatal("BaselineSHA 为空")
	}
	if err = os.WriteFile(filepath.Join(repo.Dir, "hello.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("修改文件: %v", err)
	}
	if err = os.WriteFile(filepath.Join(repo.Dir, "new_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("新增测试文件: %v", err)
	}
	patch, err := repo.ExtractPatch()
	if err != nil {
		t.Fatalf("ExtractPatch: %v", err)
	}
	if !strings.Contains(patch, "v2") || !strings.Contains(patch, "new_test.go") {
		t.Fatalf("补丁内容不完整:\n%s", patch)
	}

	patchFile := filepath.Join(t.TempDir(), "change.patch")
	if err = os.WriteFile(patchFile, []byte(patch), 0o644); err != nil {
		t.Fatalf("写补丁: %v", err)
	}
	second, err := Materialize(fixture, filepath.Join(t.TempDir(), "ws2"), patchFile)
	if err != nil {
		t.Fatalf("应用补丁物化: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(second.Dir, "hello.txt"))
	if err != nil {
		t.Fatalf("读回文件: %v", err)
	}
	if string(got) != "v2\n" {
		t.Fatalf("文件内容 = %q", got)
	}
}

func TestRemoveNewTestFilesKeepsBaseline(t *testing.T) {
	fixture := newFixture(t)
	repo, err := Materialize(fixture, filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if err = os.WriteFile(filepath.Join(repo.Dir, "extra_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("写文件: %v", err)
	}
	removed, err := repo.RemoveNewTestFiles()
	if err != nil {
		t.Fatalf("RemoveNewTestFiles: %v", err)
	}
	if len(removed) != 1 || removed[0] != "extra_test.go" {
		t.Fatalf("removed = %v", removed)
	}
	if _, err = os.Stat(filepath.Join(repo.Dir, "extra_test.go")); !os.IsNotExist(err) {
		t.Fatal("新增测试文件应被删除")
	}
}

func TestInvalidPatchFails(t *testing.T) {
	fixture := newFixture(t)
	bad := filepath.Join(t.TempDir(), "bad.patch")
	if err := os.WriteFile(bad, []byte("not a patch\n"), 0o644); err != nil {
		t.Fatalf("写坏补丁: %v", err)
	}
	if _, err := Materialize(fixture, filepath.Join(t.TempDir(), "ws"), bad); err == nil {
		t.Fatal("非法补丁必须报错")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd eval && go test ./internal/workspace/ -v`
Expected: FAIL，`undefined: Materialize`

- [ ] **Step 3: 实现 git 封装**

`eval/internal/workspace/git.go`：

```go
package workspace

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// runGit 在 dir 中执行 git 命令。
func runGit(dir string, args ...string) error {
	_, err := outputGit(dir, args...)
	return err
}

// outputGit 在 dir 中执行 git 命令并返回 stdout。
func outputGit(dir string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", dir}, args...)
	//nolint:gosec // 参数全部由评测器构造，不包含用户输入
	cmd := exec.Command("git", fullArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
```

- [ ] **Step 4: 实现 workspace.go**

`eval/internal/workspace/workspace.go`：

```go
// Package workspace 负责从 fixture 物化隔离的 git 工作区。
package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	evalUserName  = "euler-eval"
	evalUserEmail = "eval@local"
	gitDirName    = ".git"
)

// Repo 是一个物化后的评测工作区。
type Repo struct {
	// Dir 是工作区根目录。
	Dir string
	// BaselineSHA 是最后一批补丁应用后的提交。
	BaselineSHA string
}

// Materialize 复制 fixture 并依次应用补丁，返回带基线 SHA 的仓库。
func Materialize(fixtureDir, dstDir string, patches ...string) (*Repo, error) {
	if err := CopyDir(fixtureDir, dstDir); err != nil {
		return nil, err
	}
	if err := runGit(dstDir, "init", "-q"); err != nil {
		return nil, err
	}
	if err := runGit(dstDir, "config", "user.name", evalUserName); err != nil {
		return nil, err
	}
	if err := runGit(dstDir, "config", "user.email", evalUserEmail); err != nil {
		return nil, err
	}
	if err := runGit(dstDir, "add", "-A"); err != nil {
		return nil, err
	}
	if err := runGit(dstDir, "commit", "-q", "-m", "baseline"); err != nil {
		return nil, err
	}
	repo := &Repo{Dir: dstDir}
	for _, patch := range patches {
		if err := repo.ApplyFile(patch); err != nil {
			return nil, err
		}
		if err := runGit(dstDir, "add", "-A"); err != nil {
			return nil, err
		}
		status, err := outputGit(dstDir, "status", "--porcelain")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(status) == "" {
			continue // 空补丁或补丁未产生改动，跳过空提交
		}
		if err = runGit(dstDir, "commit", "-q", "-m", "apply "+filepath.Base(patch)); err != nil {
			return nil, err
		}
	}
	sha, err := outputGit(dstDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	repo.BaselineSHA = strings.TrimSpace(sha)
	return repo, nil
}

// ApplyFile 用 --3way 应用补丁，不创建提交；空补丁视为无操作。
func (r *Repo) ApplyFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取补丁 %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	if err = runGit(r.Dir, "apply", "--3way", "--whitespace=nowarn", path); err != nil {
		return fmt.Errorf("应用补丁 %s: %w", path, err)
	}
	return nil
}

// ExtractPatch 提取工作区相对基线 SHA 的完整补丁。
func (r *Repo) ExtractPatch() (string, error) {
	if err := runGit(r.Dir, "add", "-A"); err != nil {
		return "", err
	}
	patch, err := outputGit(r.Dir, "diff", "--cached", "--binary", r.BaselineSHA)
	if err != nil {
		return "", fmt.Errorf("提取补丁: %w", err)
	}
	return patch, nil
}

// Restore 将指定文件还原为基线版本。
func (r *Repo) Restore(path string) error {
	if err := runGit(r.Dir, "checkout", r.BaselineSHA, "--", path); err != nil {
		return fmt.Errorf("还原 %s: %w", path, err)
	}
	return nil
}

// RemoveNewTestFiles 删除基线中不存在的 *_test.go，返回被删除的相对路径。
func (r *Repo) RemoveNewTestFiles() ([]string, error) {
	out, err := outputGit(r.Dir, "ls-tree", "-r", "--name-only", r.BaselineSHA)
	if err != nil {
		return nil, err
	}
	baseline := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			baseline[line] = struct{}{}
		}
	}
	removed := make([]string, 0)
	walkErr := filepath.WalkDir(r.Dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == gitDirName {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(r.Dir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if _, ok := baseline[rel]; ok {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return removeErr
		}
		removed = append(removed, rel)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	if len(removed) > 0 {
		if err := runGit(r.Dir, "add", "-A"); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

// CopyDir 递归复制普通文件，跳过 .git。
func CopyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			if entry.Name() == gitDirName {
				return fs.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o644)
	})
}
```

注意：`Materialize` 里先 `ApplyFile` 再 commit，因此 `ApplyFile` 的 `--3way` 在干净工作树上等价于普通 apply；Judge 中复用同一方法应用 Agent 补丁，不提交。

- [ ] **Step 5: 运行测试**

Run: `cd eval && go test ./internal/workspace/ -v`
Expected: PASS（3 个测试）

- [ ] **Step 6: 检查点**

Run: `cd eval && gofmt -l . && go vet ./...`
Expected: 均无输出。

---

### Task 3: 题目完整校验与 `tasks validate` CLI

**Files:**
- Create: `eval/internal/task/validate.go`
- Create: `eval/cmd/euleval/main.go`
- Test: `eval/internal/task/validate_test.go`

**Interfaces:**
- Consumes: `task.Task`、`workspace.Materialize`
- Produces:
  - `task.ValidatePatches(t Task, fixtureDir, workRoot string) error`
  - CLI：`euleval tasks validate --tasks DIR --fixture DIR`

- [ ] **Step 1: 写失败测试**

`eval/internal/task/validate_test.go`：

```go
package task

import (
	"os"
	"path/filepath"
	"testing"
)

// commitFixture 用 workspace 无关的方式写出一个最小 fixture 与两道补丁。
func preparePatches(t *testing.T, dir string) (fixture string) {
	t.Helper()
	fixture = filepath.Join(dir, "fixture")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatalf("建 fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "a.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("写 fixture 文件: %v", err)
	}
	taskDir := filepath.Join(dir, "tasks", "demo-001")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatalf("建题目目录: %v", err)
	}
	// 用 git 生成真实补丁，避免手写 index 行。
	repo, err := materializeForTest(fixture, filepath.Join(dir, "seeded"))
	if err != nil {
		t.Fatalf("种子仓库: %v", err)
	}
	if err = os.WriteFile(filepath.Join(repo.Dir, "a.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("写 buggy 内容: %v", err)
	}
	if err = os.WriteFile(filepath.Join(taskDir, "bug.patch"), []byte(mustPatch(t, repo)), 0o644); err != nil {
		t.Fatalf("写 bug.patch: %v", err)
	}
	if err = os.WriteFile(filepath.Join(taskDir, "test.patch"), []byte(""), 0o644); err != nil {
		t.Fatalf("写 test.patch: %v", err)
	}
	if err = os.WriteFile(filepath.Join(taskDir, "gold.patch"), []byte(""), 0o644); err != nil {
		t.Fatalf("写 gold.patch: %v", err)
	}
	return fixture
}

func TestValidatePatchesRejectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	fixture := preparePatches(t, dir)
	value := Task{
		ID: "demo-001", Issue: "x",
		FailToPass: []string{"TestA"}, PassToPass: []string{"TestB"},
		Dir: filepath.Join(dir, "tasks", "demo-001"),
	}.WithDefaults()
	if err := os.Remove(value.GoldPatch()); err != nil {
		t.Fatalf("删除 gold: %v", err)
	}
	if err := ValidatePatches(value, fixture, t.TempDir()); err == nil {
		t.Fatal("缺失补丁必须报错")
	}
}
```

在同一测试目录新增 `eval/internal/task/validate_test_helpers_test.go`：

```go
package task

import (
	"os"
	"path/filepath"
	"testing"

	"euler/eval/internal/workspace"
)

func materializeForTest(fixture, dst string) (*workspace.Repo, error) {
	return workspace.Materialize(fixture, dst)
}

func mustPatch(t *testing.T, repo *workspace.Repo) string {
	t.Helper()
	patch, err := repo.ExtractPatch()
	if err != nil {
		t.Fatalf("提取补丁: %v", err)
	}
	return patch
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd eval && go test ./internal/task/ -run TestValidatePatches -v`
Expected: FAIL，`undefined: ValidatePatches`

- [ ] **Step 3: 实现 validate.go**

`eval/internal/task/validate.go`：

```go
package task

import (
	"fmt"
	"os"
	"path/filepath"

	"euler/eval/internal/workspace"
)

// ValidatePatches 校验三个补丁能按预期应用：bug+test、bug+gold。
func ValidatePatches(t Task, fixtureDir, workRoot string) error {
	for _, path := range []string{t.BugPatch(), t.TestPatch(), t.GoldPatch()} {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%w: %s: 补丁缺失 %s", ErrInvalid, t.ID, path)
		}
	}
	if err := validatePair(t, fixtureDir, workRoot, "with-test", t.TestPatch()); err != nil {
		return err
	}
	return validatePair(t, fixtureDir, workRoot, "with-gold", t.GoldPatch())
}

func validatePair(t Task, fixtureDir, workRoot, suffix, second string) error {
	dst := filepath.Join(workRoot, t.ID+"-"+suffix)
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dst) }()
	repo, err := workspace.Materialize(fixtureDir, dst, t.BugPatch())
	if err != nil {
		return fmt.Errorf("%w: %s: bug.patch 应用失败: %v", ErrInvalid, t.ID, err)
	}
	if err = repo.ApplyFile(second); err != nil {
		return fmt.Errorf("%w: %s: 补丁 %s 应用失败: %v", ErrInvalid, t.ID, second, err)
	}
	return nil
}
```

说明：`defer` 中忽略错误需要 `//nolint:errcheck` 或直接 `_ =`，此处用 `_ =` 已显式处理。

- [ ] **Step 4: 实现 CLI 骨架**

`eval/cmd/euleval/main.go`：

```go
// Command euleval 是 Euler 编码能力评测工具的入口。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"euler/eval/internal/task"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "tasks":
		err = runTasks(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "euleval: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: euleval <tasks|run|judge|smoke> [flags]")
}

func runTasks(args []string) error {
	if len(args) == 0 || args[0] != "validate" {
		return fmt.Errorf("用法: euleval tasks validate --tasks DIR --fixture DIR")
	}
	fs := flag.NewFlagSet("tasks validate", flag.ContinueOnError)
	tasksDir := fs.String("tasks", "./tasks", "题库目录")
	fixtureDir := fs.String("fixture", "./fixture", "fixture 仓库目录")
	only := fs.String("only", "", "只校验指定题目，逗号分隔")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tasks, err := task.Load(*tasksDir)
	if err != nil {
		return err
	}
	ids := splitIDs(*only)
	tasks, err = task.Filter(tasks, ids)
	if err != nil {
		return err
	}
	workRoot, err := os.MkdirTemp("", "euleval-validate-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workRoot) }()
	for _, item := range tasks {
		if err = item.ValidateBasic(); err != nil {
			return err
		}
		if err = task.ValidatePatches(item, *fixtureDir, workRoot); err != nil {
			return err
		}
		fmt.Printf("OK   %s\n", item.ID)
	}
	fmt.Printf("校验完成：%d 道题\n", len(tasks))
	return nil
}

func splitIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
```

- [ ] **Step 5: 运行测试与构建**

Run: `cd eval && go test ./internal/task/ -v && go build ./...`
Expected: 测试 PASS；构建成功。

- [ ] **Step 6: 检查点**

Run: `cd eval && gofmt -l . && go vet ./...`
Expected: 无输出。

---

### Task 4: fixture 练习仓库

**Files:**
- Create: `eval/fixture/go.mod`
- Create: `eval/fixture/.golangci.yml`
- Create: `eval/fixture/textutil/textutil.go`
- Create: `eval/fixture/textutil/textutil_test.go`
- Create: `eval/fixture/calculator/calculator.go`
- Create: `eval/fixture/calculator/calculator_test.go`
- Create: `eval/fixture/store/store.go`
- Create: `eval/fixture/store/store_test.go`
- Create: `eval/fixture/state/state.go`
- Create: `eval/fixture/state/state_test.go`

**Interfaces:**
- Produces（供题库补丁引用）：
  - `textutil.Split(s, sep string) []string`、`textutil.Join(parts []string, sep string) string`
  - `calculator.Eval(expr string) (int, error)`、`ErrDivideByZero`、`ErrInvalidExpression`
  - `store.New() *Store`、`Set/Get/Delete/Sum`
  - `state.New() *Order`、`Pay/Ship/Cancel/Status`、`ErrInvalidTransition`

- [ ] **Step 1: 写 module 与 lint 配置**

`eval/fixture/go.mod`：

```go
module example.com/euler-fixture

go 1.26.5
```

`eval/fixture/.golangci.yml`：

```yaml
# Euler fixture 是评测题目载体，只保留最小基线检查。
version: "2"

linters:
  default: standard

formatters:
  enable:
    - gofmt
    - goimports
```

- [ ] **Step 2: 写 textutil**

`eval/fixture/textutil/textutil.go`：

```go
// Package textutil 提供字符串切分与拼接工具。
package textutil

import (
	"strings"
	"unicode/utf8"
)

// Split 按 sep 切分 s。sep 为空时按 UTF-8 字符逐个切分，s 为空时返回 nil。
func Split(s, sep string) []string {
	if s == "" {
		return nil
	}
	if sep == "" {
		return splitRunes(s)
	}
	return strings.Split(s, sep)
}

// Join 用 sep 拼接 parts。
func Join(parts []string, sep string) string {
	return strings.Join(parts, sep)
}

func splitRunes(s string) []string {
	out := make([]string, 0, utf8.RuneCountInString(s))
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}
```

`eval/fixture/textutil/textutil_test.go`：

```go
package textutil

import (
	"reflect"
	"testing"
)

func TestSplitBasic(t *testing.T) {
	got := Split("a,b,c", ",")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Split = %v, want %v", got, want)
	}
}

func TestSplitEmptyInput(t *testing.T) {
	if got := Split("", ","); got != nil {
		t.Fatalf("Split(空) = %v, 期望 nil", got)
	}
}

func TestJoin(t *testing.T) {
	if got := Join([]string{"a", "b"}, "-"); got != "a-b" {
		t.Fatalf("Join = %q", got)
	}
}
```

- [ ] **Step 3: 写 calculator**

`eval/fixture/calculator/calculator.go`：

```go
// Package calculator 计算仅含 + - * / 与括号的整数表达式。
package calculator

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
)

// ErrInvalidExpression 表示表达式无法解析。
var ErrInvalidExpression = errors.New("invalid expression")

// ErrDivideByZero 表示除数为零。
var ErrDivideByZero = errors.New("divide by zero")

// Eval 计算整数表达式。
func Eval(expr string) (int, error) {
	p := &parser{input: strings.ReplaceAll(expr, " ", "")}
	value, err := p.parseSum()
	if err != nil {
		return 0, err
	}
	if p.pos != len(p.input) {
		return 0, ErrInvalidExpression
	}
	return value, nil
}

type parser struct {
	input string
	pos   int
}

func (p *parser) parseSum() (int, error) {
	value, err := p.parseProduct()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.input) {
		op := p.input[p.pos]
		if op != '+' && op != '-' {
			return value, nil
		}
		p.pos++
		right, err := p.parseProduct()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			value += right
		} else {
			value -= right
		}
	}
	return value, nil
}

func (p *parser) parseProduct() (int, error) {
	value, err := p.parsePrimary()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.input) {
		op := p.input[p.pos]
		if op != '*' && op != '/' {
			return value, nil
		}
		p.pos++
		right, err := p.parsePrimary()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			value *= right
			continue
		}
		if right == 0 {
			return 0, ErrDivideByZero
		}
		value /= right
	}
	return value, nil
}

func (p *parser) parsePrimary() (int, error) {
	start := p.pos
	if p.pos >= len(p.input) {
		return 0, ErrInvalidExpression
	}
	if p.input[p.pos] == '(' {
		p.pos++
		value, err := p.parseSum()
		if err != nil {
			return 0, err
		}
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return 0, ErrInvalidExpression
		}
		p.pos++
		return value, nil
	}
	for p.pos < len(p.input) && unicode.IsDigit(rune(p.input[p.pos])) {
		p.pos++
	}
	if p.pos == start {
		return 0, ErrInvalidExpression
	}
	value, err := strconv.Atoi(p.input[start:p.pos])
	if err != nil {
		return 0, ErrInvalidExpression
	}
	return value, nil
}
```

`eval/fixture/calculator/calculator_test.go`：

```go
package calculator

import (
	"errors"
	"testing"
)

func TestEvalPrecedence(t *testing.T) {
	got, err := Eval("1+2*3")
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got != 7 {
		t.Fatalf("Eval = %d, want 7", got)
	}
}

func TestEvalParens(t *testing.T) {
	got, err := Eval("(1+2)*3")
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got != 9 {
		t.Fatalf("Eval = %d, want 9", got)
	}
}

func TestEvalDivideByZero(t *testing.T) {
	if _, err := Eval("1/0"); !errors.Is(err, ErrDivideByZero) {
		t.Fatalf("err = %v, want ErrDivideByZero", err)
	}
}

func TestEvalInvalid(t *testing.T) {
	if _, err := Eval("1+"); !errors.Is(err, ErrInvalidExpression) {
		t.Fatalf("err = %v, want ErrInvalidExpression", err)
	}
}
```

- [ ] **Step 4: 写 store**

`eval/fixture/store/store.go`：

```go
// Package store 提供并发安全的内存 KV。
package store

import "sync"

// Store 是并发安全的内存 KV。
type Store struct {
	mu   sync.RWMutex
	data map[string]int
}

// New 创建空 Store。
func New() *Store { return &Store{data: make(map[string]int)} }

// Set 写入键值。
func (s *Store) Set(key string, value int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

// Get 读取键值。
func (s *Store) Get(key string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.data[key]
	return value, ok
}

// Delete 删除键。
func (s *Store) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// Sum 返回所有值之和。
func (s *Store) Sum() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, value := range s.data {
		total += value
	}
	return total
}
```

`eval/fixture/store/store_test.go`：

```go
package store

import "testing"

func TestSetGet(t *testing.T) {
	s := New()
	s.Set("a", 1)
	if value, ok := s.Get("a"); !ok || value != 1 {
		t.Fatalf("Get = %d, %v", value, ok)
	}
}

func TestDeleteMissing(t *testing.T) {
	s := New()
	s.Delete("missing")
	if _, ok := s.Get("missing"); ok {
		t.Fatal("删除不存在的键不应产生值")
	}
}

func TestSum(t *testing.T) {
	s := New()
	s.Set("a", 1)
	s.Set("b", 2)
	if got := s.Sum(); got != 3 {
		t.Fatalf("Sum = %d, want 3", got)
	}
}
```

- [ ] **Step 5: 写 state**

`eval/fixture/state/state.go`：

```go
// Package state 实现订单状态机。
package state

import "errors"

// Status 是订单状态。
type Status string

const (
	// StatusCreated 表示订单已创建。
	StatusCreated Status = "created"
	// StatusPaid 表示订单已支付。
	StatusPaid Status = "paid"
	// StatusShipped 表示订单已发货。
	StatusShipped Status = "shipped"
	// StatusCancelled 表示订单已取消。
	StatusCancelled Status = "cancelled"
)

// ErrInvalidTransition 表示非法的状态迁移。
var ErrInvalidTransition = errors.New("invalid transition")

// Order 是订单聚合。
type Order struct{ status Status }

// New 创建处于 created 状态的订单。
func New() *Order { return &Order{status: StatusCreated} }

// Status 返回当前状态。
func (o *Order) Status() Status { return o.status }

// Pay 将订单置为已支付，仅允许从 created 迁移。
func (o *Order) Pay() error {
	if o.status != StatusCreated {
		return ErrInvalidTransition
	}
	o.status = StatusPaid
	return nil
}

// Ship 将订单置为已发货，仅允许从 paid 迁移。
func (o *Order) Ship() error {
	if o.status != StatusPaid {
		return ErrInvalidTransition
	}
	o.status = StatusShipped
	return nil
}

// Cancel 取消订单，已发货或已取消时拒绝。
func (o *Order) Cancel() error {
	if o.status == StatusShipped || o.status == StatusCancelled {
		return ErrInvalidTransition
	}
	o.status = StatusCancelled
	return nil
}
```

`eval/fixture/state/state_test.go`：

```go
package state

import "testing"

func TestPayThenShip(t *testing.T) {
	order := New()
	if err := order.Pay(); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if err := order.Ship(); err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if order.Status() != StatusShipped {
		t.Fatalf("Status = %s", order.Status())
	}
}

func TestCancelAfterPaid(t *testing.T) {
	order := New()
	if err := order.Pay(); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if err := order.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if order.Status() != StatusCancelled {
		t.Fatalf("Status = %s", order.Status())
	}
}

func TestCancelFromCreated(t *testing.T) {
	order := New()
	if err := order.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}
```

- [ ] **Step 6: 运行 fixture 测试**

Run: `cd eval/fixture && go test ./... && go vet ./...`
Expected: 4 个包全部 PASS，vet 无输出。

- [ ] **Step 7: 检查点**

Run: `cd eval/fixture && gofmt -l .`
Expected: 无输出。

---

### Task 5: 五道题与三个补丁

**Files:**
- Create: `eval/tasks/textutil-001/{task.json,bug.patch,test.patch,gold.patch}`
- Create: `eval/tasks/calculator-001/{task.json,bug.patch,test.patch,gold.patch}`
- Create: `eval/tasks/calculator-002/{task.json,bug.patch,test.patch,gold.patch}`
- Create: `eval/tasks/store-001/{task.json,bug.patch,test.patch,gold.patch}`
- Create: `eval/tasks/state-001/{task.json,bug.patch,test.patch,gold.patch}`

**Interfaces:**
- Consumes: `eval/fixture` 的四个包
- Produces: 可被 `euleval tasks validate` 全部通过的题库

补丁生成配方（所有题通用，在 `eval/` 下执行）：

```bash
# 1) 建立种子仓库
SEED=$(mktemp -d)
cp -R fixture/. "$SEED"
git -C "$SEED" init -q
git -C "$SEED" add -A
git -C "$SEED" commit -qm baseline

# 2) 编辑出 buggy 版本后，生成 bug.patch
git -C "$SEED" diff > tasks/<id>/bug.patch
git -C "$SEED" add -A && git -C "$SEED" commit -qm buggy

# 3) 在 buggy 版本上编辑出修复版本，生成 gold.patch
git -C "$SEED" diff > tasks/<id>/gold.patch

# 4) 恢复 buggy 版本，新增隐藏测试文件，生成 test.patch
git -C "$SEED" checkout -- .
git -C "$SEED" add -N <隐藏测试文件相对路径>
git -C "$SEED" diff > tasks/<id>/test.patch
git -C "$SEED" checkout -- . && git -C "$SEED" clean -fd
```

- [ ] **Step 1: 生成 textutil-001（边界条件/panic）**

`task.json`：

```json
{
  "id": "textutil-001",
  "title": "Split 在空分隔符下按字节切分",
  "issue": "调用 textutil.Split(\"中文\", \"\") 返回了乱码片段，而文档承诺按字符切分。请修复空分隔符分支，使中文与 ASCII 都按 UTF-8 字符切分，并保持其他切分与 Join 行为不变。",
  "difficulty": "easy",
  "tags": ["edge-case", "unicode"],
  "protected_tests": ["textutil/textutil_test.go"],
  "fail_to_pass": ["TestSplitEmptySepUnicode"],
  "pass_to_pass": ["TestSplitBasic", "TestSplitEmptyInput", "TestJoin"]
}
```

buggy 编辑（[textutil.go](file:///Users/jason/go/src/Euler/eval/fixture/textutil/textutil.go) 的 `Split` 整体替换为按字节切分的版本）：

```go
// Split 按 sep 切分 s。
func Split(s, sep string) []string {
	if s == "" {
		return nil
	}
	if sep == "" {
		out := make([]string, 0, len(s))
		for i := 0; i < len(s); i++ {
			out = append(out, s[i:i+1])
		}
		return out
	}
	return strings.Split(s, sep)
}
```

这样 ASCII 仍能被切成单字符（`TestSplitEmptySep` 在基线即可通过，不作为 F2P），而中文会被切成 4 个无效 UTF-8 字节片段，`TestSplitEmptySepUnicode` 在基线失败。

gold 修复：恢复为 fixture 原实现（`sep == ""` 分支调用 `splitRunes`）。

`test.patch` 新增 `textutil/hidden_split_test.go`：

```go
package textutil

import (
	"reflect"
	"testing"
)

func TestSplitEmptySep(t *testing.T) {
	got := Split("a,b", "")
	want := []string{"a", ",", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Split = %v, want %v", got, want)
	}
}

func TestSplitEmptySepUnicode(t *testing.T) {
	got := Split("中文", "")
	want := []string{"中", "文"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Split = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: 生成 calculator-001（解析逻辑/优先级）**

`task.json`：

```json
{
  "id": "calculator-001",
  "title": "乘除优先级错误",
  "issue": "calculator.Eval(\"1+2*3\") 返回 9，正确结果应为 7。请修正表达式求值的运算符优先级，同时保证括号、减法与除法行为正确。",
  "difficulty": "medium",
  "tags": ["parsing", "precedence"],
  "protected_tests": ["calculator/calculator_test.go"],
  "fail_to_pass": ["TestEvalPrecedenceMixed"],
  "pass_to_pass": ["TestEvalParens", "TestEvalDivideByZero", "TestEvalInvalid"]
}
```

buggy 编辑（把 `parseSum` 整体替换为下式，所有运算符按从左到右计算）：

```go
func (p *parser) parseSum() (int, error) {
	value, err := p.parsePrimary()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.input) {
		op := p.input[p.pos]
		if op != '+' && op != '-' && op != '*' && op != '/' {
			return value, nil
		}
		p.pos++
		right, err := p.parsePrimary()
		if err != nil {
			return 0, err
		}
		switch op {
		case '+':
			value += right
		case '-':
			value -= right
		case '*':
			value *= right
		case '/':
			if right == 0 {
				return 0, ErrDivideByZero
			}
			value /= right
		}
	}
	return value, nil
}
```

这个注入只破坏优先级：括号、除零错误与非法表达式检测仍然正确，因此这些可见测试可以继续作为 P2P。

gold 修复：恢复 fixture 原实现（`parseSum` 调用 `parseProduct`，由 `parseProduct` 处理乘除）。

`test.patch` 新增 `calculator/hidden_precedence_test.go`：

```go
package calculator

import "testing"

func TestEvalPrecedenceMixed(t *testing.T) {
	cases := map[string]int{
		"1+2*3":   7,
		"10-6/2":  7,
		"2*3+4*5": 26,
		"8/4/2":   1,
	}
	for expr, want := range cases {
		got, err := Eval(expr)
		if err != nil {
			t.Fatalf("Eval(%q): %v", expr, err)
		}
		if got != want {
			t.Fatalf("Eval(%q) = %d, want %d", expr, got, want)
		}
	}
}
```

- [ ] **Step 3: 生成 calculator-002（错误处理遗漏）**

`task.json`：

```json
{
  "id": "calculator-002",
  "title": "除零未返回错误",
  "issue": "calculator.Eval(\"1/0\") 返回了 (0, nil)。期望除数为零时返回 ErrDivideByZero，调用方据此可以提示用户。请修复，并保持现有优先级与括号行为。",
  "difficulty": "easy",
  "tags": ["error-handling"],
  "protected_tests": ["calculator/calculator_test.go"],
  "fail_to_pass": ["TestEvalDivideByZeroHidden"],
  "pass_to_pass": ["TestEvalPrecedence", "TestEvalParens", "TestEvalInvalid"]
}
```

buggy 编辑（`parseProduct` 中）：

```go
if right == 0 {
	return 0, nil
}
```

gold 修复：`return 0, ErrDivideByZero`。

`test.patch` 新增 `calculator/hidden_error_test.go`：

```go
package calculator

import (
	"errors"
	"testing"
)

func TestEvalDivideByZeroHidden(t *testing.T) {
	if _, err := Eval("7/(3-3)"); !errors.Is(err, ErrDivideByZero) {
		t.Fatalf("err = %v, want ErrDivideByZero", err)
	}
}
```

- [ ] **Step 4: 生成 store-001（并发竞争）**

`task.json`：

```json
{
  "id": "store-001",
  "title": "并发删除存在数据竞争",
  "issue": "store.Store 在并发执行 Set 与 Delete 时触发 race detector 报告。请修复并发安全问题，不改变公开 API 与现有行为。",
  "difficulty": "medium",
  "tags": ["concurrency", "race"],
  "protected_tests": ["store/store_test.go"],
  "fail_to_pass": ["TestConcurrentSetDelete"],
  "pass_to_pass": ["TestSetGet", "TestDeleteMissing", "TestSum"],
  "test_command": "go test -race -count=1 -json ./..."
}
```

buggy 编辑（`Delete` 去掉加锁）：

```go
// Delete 删除键。
func (s *Store) Delete(key string) {
	delete(s.data, key)
}
```

gold 修复：恢复 `Lock/Unlock`。

`test.patch` 新增 `store/hidden_race_test.go`：

```go
package store

import (
	"sync"
	"testing"
)

func TestConcurrentSetDelete(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s.Set("key", worker+j)
				s.Delete("key")
			}
		}(i)
	}
	wg.Wait()
}
```

- [ ] **Step 5: 生成 state-001（状态机缺陷）**

`task.json`：

```json
{
  "id": "state-001",
  "title": "已取消订单可以被发货",
  "issue": "订单取消后调用 Ship 仍然成功，这是非法状态迁移。请保证只有已支付订单可以发货，并保持创建、支付、取消的现有行为。",
  "difficulty": "easy",
  "tags": ["state-machine"],
  "protected_tests": ["state/state_test.go"],
  "fail_to_pass": ["TestShipAfterCancelFails", "TestShipFromCreatedFails"],
  "pass_to_pass": ["TestPayThenShip", "TestCancelAfterPaid", "TestCancelFromCreated"]
}
```

buggy 编辑（`Ship` 状态检查改为）：

```go
if o.status == StatusShipped {
	return ErrInvalidTransition
}
```

gold 修复：`if o.status != StatusPaid`。

`test.patch` 新增 `state/hidden_transition_test.go`：

```go
package state

import (
	"errors"
	"testing"
)

func TestShipAfterCancelFails(t *testing.T) {
	order := New()
	if err := order.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := order.Ship(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Ship = %v, want ErrInvalidTransition", err)
	}
}

func TestShipFromCreatedFails(t *testing.T) {
	order := New()
	if err := order.Ship(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Ship = %v, want ErrInvalidTransition", err)
	}
}
```

- [ ] **Step 6: 校验题库**

Run: `cd eval && go run ./cmd/euleval tasks validate --tasks ./tasks --fixture ./fixture`
Expected: 5 行 `OK`，最后输出 `校验完成：5 道题`

- [ ] **Step 7: 检查点**

Run: `cd eval && git status --short`
Expected: 新增 20 个题目文件；无 `results/` 或临时目录。

---

### Task 6: report 包

**Files:**
- Create: `eval/internal/report/report.go`
- Test: `eval/internal/report/report_test.go`

**Interfaces:**
- Produces:
  - `report.Prediction{InstanceID, ModelNameOrPath, ModelPatch, Meta}`
  - `report.Meta{Status, RunID, DurationMS, ToolCalls, Approvals, DiffFiles, DiffAdded, DiffDeleted, TouchedTestFiles, Error}`
  - `report.TestSummary{Total, Passed int; Failed []string}`
  - `report.TaskResult{InstanceID, Resolved, Reason, BuildOK, FailToPass, PassToPass}`
  - `report.Report{StartedAt, FinishedAt string; Totals Totals; Tasks []TaskResult}`
  - `report.Totals`（字段见代码）
  - `report.WritePredictions(path string, preds []Prediction) error`
  - `report.AppendPrediction(path string, pred Prediction) error`
  - `report.ReadPredictions(path string) ([]Prediction, error)`
  - `report.WriteReport(path string, r Report) error`
  - `report.Summarize(results []TaskResult, preds []Prediction) Totals`
  - `report.PrintSummary(w io.Writer, r Report)`

- [ ] **Step 1: 写失败测试**

`eval/internal/report/report_test.go`：

```go
package report

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestPredictionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.jsonl")
	want := Prediction{
		InstanceID: "demo-001", ModelNameOrPath: "euler-local",
		ModelPatch: "diff --git a/a b/a\n",
		Meta:       Meta{Status: "completed", ToolCalls: 3},
	}
	if err := WritePredictions(path, []Prediction{want}); err != nil {
		t.Fatalf("WritePredictions: %v", err)
	}
	got, err := ReadPredictions(path)
	if err != nil {
		t.Fatalf("ReadPredictions: %v", err)
	}
	if len(got) != 1 || got[0].InstanceID != want.InstanceID || got[0].Meta.ToolCalls != 3 {
		t.Fatalf("got = %+v", got)
	}
}

func TestSummarize(t *testing.T) {
	results := []TaskResult{
		{InstanceID: "a", Resolved: true},
		{InstanceID: "b", Resolved: false, Reason: "patch_failed"},
		{InstanceID: "c", Resolved: false, Reason: "build_failed"},
	}
	preds := []Prediction{
		{InstanceID: "d", Meta: Meta{Status: "error"}},
		{InstanceID: "e", Meta: Meta{Status: "completed"}},
	}
	totals := Summarize(results, preds)
	if totals.Tasks != 5 || totals.Resolved != 1 || totals.PatchFailed != 1 ||
		totals.BuildFailed != 1 || totals.Error != 1 {
		t.Fatalf("totals = %+v", totals)
	}
}

func TestPrintSummaryContainsRows(t *testing.T) {
	var buf bytes.Buffer
	report := Report{
		Totals: Totals{Tasks: 1, Resolved: 1, ResolvedRate: 1},
		Tasks: []TaskResult{{
			InstanceID: "demo-001", Resolved: true,
			FailToPass: TestSummary{Total: 2, Passed: 2},
			PassToPass: TestSummary{Total: 2, Passed: 2},
		}},
	}
	PrintSummary(&buf, report)
	out := buf.String()
	if !strings.Contains(out, "PASS") || !strings.Contains(out, "demo-001") {
		t.Fatalf("输出 = %q", out)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `cd eval && go test ./internal/report/ -v`
Expected: FAIL，`undefined: Prediction`

- [ ] **Step 3: 实现 report.go**

`eval/internal/report/report.go`：

```go
// Package report 负责评测结果的读写与摘要输出。
package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// Meta 是 Runner 记录的每题元信息。
type Meta struct {
	Status           string `json:"status"`
	RunID            string `json:"run_id,omitempty"`
	DurationMS       int64  `json:"duration_ms"`
	ToolCalls        int    `json:"tool_calls"`
	Approvals        int    `json:"approvals"`
	DiffFiles        int    `json:"diff_files"`
	DiffAdded        int    `json:"diff_added"`
	DiffDeleted      int    `json:"diff_deleted"`
	TouchedTestFiles bool   `json:"touched_test_files"`
	Error            string `json:"error,omitempty"`
}

// Prediction 是 preds.jsonl 的一行。
type Prediction struct {
	InstanceID      string `json:"instance_id"`
	ModelNameOrPath string `json:"model_name_or_path"`
	ModelPatch      string `json:"model_patch"`
	Meta            Meta   `json:"meta"`
}

// TestSummary 是一组测试的通过情况。
type TestSummary struct {
	Total  int      `json:"total"`
	Passed int      `json:"passed"`
	Failed []string `json:"failed"`
}

// Metrics 是题目级工程指标。
type Metrics struct {
	DurationMS int64 `json:"duration_ms"`
	ToolCalls  int   `json:"tool_calls"`
	DiffFiles  int   `json:"diff_files"`
}

// TaskResult 是单题判分结果。
type TaskResult struct {
	InstanceID string      `json:"instance_id"`
	Resolved   bool        `json:"resolved"`
	Reason     string      `json:"reason,omitempty"`
	BuildOK    bool        `json:"build_ok"`
	FailToPass TestSummary `json:"fail_to_pass"`
	PassToPass TestSummary `json:"pass_to_pass"`
	Metrics    Metrics     `json:"metrics"`
}

// Totals 是聚合统计。
type Totals struct {
	Tasks        int     `json:"tasks"`
	Resolved     int     `json:"resolved"`
	ResolvedRate float64 `json:"resolved_rate"`
	PatchFailed  int     `json:"patch_failed"`
	NoChanges    int     `json:"no_changes"`
	BuildFailed  int     `json:"build_failed"`
	Error        int     `json:"error"`
}

// Report 是 report.json 的完整内容。
type Report struct {
	StartedAt  string       `json:"started_at"`
	FinishedAt string       `json:"finished_at"`
	Totals     Totals       `json:"totals"`
	Tasks      []TaskResult `json:"tasks"`
}

// WritePredictions 覆盖写入 preds.jsonl。
func WritePredictions(path string, preds []Prediction) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建 %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	writer := bufio.NewWriter(file)
	for _, pred := range preds {
		data, marshalErr := json.Marshal(pred)
		if marshalErr != nil {
			return marshalErr
		}
		if _, writeErr := writer.WriteString(string(data) + "\n"); writeErr != nil {
			return writeErr
		}
	}
	return writer.Flush()
}

// AppendPrediction 向 preds.jsonl 追加一行。
func AppendPrediction(path string, pred Prediction) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开 %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := json.Marshal(pred)
	if err != nil {
		return err
	}
	_, err = file.WriteString(string(data) + "\n")
	return err
}

// ReadPredictions 读取 preds.jsonl。
func ReadPredictions(path string) ([]Prediction, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	preds := make([]Prediction, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var pred Prediction
		if err = json.Unmarshal([]byte(line), &pred); err != nil {
			return nil, fmt.Errorf("解析 %s: %w", path, err)
		}
		preds = append(preds, pred)
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	return preds, nil
}

// WriteReport 写入 report.json。
func WriteReport(path string, value Report) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Summarize 汇总判分结果与 Runner 状态。
func Summarize(results []TaskResult, preds []Prediction) Totals {
	totals := Totals{Tasks: len(results)}
	for _, result := range results {
		if result.Resolved {
			totals.Resolved++
			continue
		}
		switch result.Reason {
		case "patch_failed":
			totals.PatchFailed++
		case "build_failed":
			totals.BuildFailed++
		case "no_changes":
			totals.NoChanges++
		}
	}
	for _, pred := range preds {
		if pred.Meta.Status == "error" {
			totals.Error++
		}
	}
	if totals.Tasks > 0 {
		totals.ResolvedRate = float64(totals.Resolved) / float64(totals.Tasks)
	}
	return totals
}

// PrintSummary 输出逐题摘要与控制台总计。
func PrintSummary(w io.Writer, value Report) {
	rows := make([]TaskResult, len(value.Tasks))
	copy(rows, value.Tasks)
	sort.Slice(rows, func(i, j int) bool { return rows[i].InstanceID < rows[j].InstanceID })
	for _, row := range rows {
		mark := "FAIL"
		if row.Resolved {
			mark = "PASS"
		}
		fmt.Fprintf(w, "%-4s  %-20s  F2P %d/%d  P2P %d/%d  %dms  %d calls  %s\n",
			mark, row.InstanceID,
			row.FailToPass.Passed, row.FailToPass.Total,
			row.PassToPass.Passed, row.PassToPass.Total,
			row.Metrics.DurationMS, row.Metrics.ToolCalls,
			row.Reason)
	}
	fmt.Fprintf(w, "总计: %d/%d resolved (%.1f%%)\n",
		value.Totals.Resolved, value.Totals.Tasks, value.Totals.ResolvedRate*100)
}
```

- [ ] **Step 4: 运行测试**

Run: `cd eval && go test ./internal/report/ -v`
Expected: PASS

- [ ] **Step 5: 检查点**

Run: `cd eval && gofmt -l . && go vet ./...`
Expected: 无输出。

---

### Task 7: Judge 判分器

**Files:**
- Create: `eval/internal/judge/gotest.go`
- Create: `eval/internal/judge/judge.go`
- Test: `eval/internal/judge/gotest_test.go`
- Test: `eval/internal/judge/judge_test.go`

**Interfaces:**
- Consumes: `task.Task`、`workspace.Repo`、`report.TestSummary`
- Produces:
  - `judge.Options{FixtureDir, WorkRoot string; KeepWorkdir bool; BuildTimeout, TestTimeout time.Duration}`
  - `judge.Result{InstanceID string; Resolved bool; Reason string; BuildOK bool; FailToPass, PassToPass report.TestSummary}`
  - `judge.Run(t task.Task, modelPatch string, opts Options) (Result, error)`
  - `judge.parseTestOutput(reader io.Reader) testOutcomes`（包内）

- [ ] **Step 1: 写解析测试**

`eval/internal/judge/gotest_test.go`：

```go
package judge

import (
	"strings"
	"testing"
)

func TestParseTestOutput(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"run","Package":"example/a","Test":"TestA"}`,
		`{"Action":"pass","Package":"example/a","Test":"TestA"}`,
		`{"Action":"run","Package":"example/b","Test":"TestB"}`,
		`{"Action":"fail","Package":"example/b","Test":"TestB"}`,
		`not json line`,
	}, "\n")
	outcomes := parseTestOutput(strings.NewReader(input))
	if status, ok := outcomes.lookup("TestA"); !ok || status != "pass" {
		t.Fatalf("TestA = %q, %v", status, ok)
	}
	if status, ok := outcomes.lookup("example/b::TestB"); !ok || status != "fail" {
		t.Fatalf("TestB = %q, %v", status, ok)
	}
	if _, ok := outcomes.lookup("TestMissing"); ok {
		t.Fatal("不存在的测试不应被找到")
	}
}

func TestSummarizeTests(t *testing.T) {
	outcomes := parseTestOutput(strings.NewReader(strings.Join([]string{
		`{"Action":"pass","Package":"p","Test":"TestA"}`,
		`{"Action":"fail","Package":"p","Test":"TestB"}`,
	}, "\n")))
	summary := summarizeTests([]string{"TestA", "TestB", "TestC"}, outcomes)
	if summary.Total != 3 || summary.Passed != 1 || len(summary.Failed) != 2 {
		t.Fatalf("summary = %+v", summary)
	}
}
```

- [ ] **Step 2: 实现 gotest.go**

`eval/internal/judge/gotest.go`：

```go
package judge

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"euler/eval/internal/report"
)

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

type testOutcomes struct {
	results map[string]string
}

// parseTestOutput 解析 go test -json 流，按 "包::测试" 记录状态。
func parseTestOutput(reader io.Reader) testOutcomes {
	outcomes := testOutcomes{results: make(map[string]string)}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var event testEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Test == "" || event.Action == "" {
			continue
		}
		if event.Action != "pass" && event.Action != "fail" && event.Action != "skip" {
			continue
		}
		outcomes.results[event.Package+"::"+event.Test] = event.Action
	}
	return outcomes
}

// lookup 支持 "TestX" 与 "包路径::TestX" 两种写法。
func (o testOutcomes) lookup(spec string) (string, bool) {
	if strings.Contains(spec, "::") {
		status, ok := o.results[spec]
		return status, ok
	}
	status := ""
	found := false
	for key, value := range o.results {
		if !strings.HasSuffix(key, "::"+spec) {
			continue
		}
		if found && status != value {
			return "", false // 同名测试状态冲突，视为无法判定
		}
		status, found = value, true
	}
	return status, found
}

// summarizeTests 按期望测试名列表给出通过情况。
func summarizeTests(specs []string, outcomes testOutcomes) report.TestSummary {
	summary := report.TestSummary{Total: len(specs)}
	for _, spec := range specs {
		status, ok := outcomes.lookup(spec)
		if ok && status == "pass" {
			summary.Passed++
			continue
		}
		summary.Failed = append(summary.Failed, spec)
	}
	return summary
}
```

- [ ] **Step 3: 写 Judge 测试（真实 go test）**

`eval/internal/judge/judge_test.go`：

```go
package judge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"euler/eval/internal/task"
	"euler/eval/internal/workspace"
)

const (
	miniCorrect = "package mini\n\n// Add 返回两数之和。\nfunc Add(a, b int) int { return a + b }\n"
	miniBuggy   = "package mini\n\n// Add 返回两数之和。\nfunc Add(a, b int) int { return a - b }\n"
	// 可见测试只覆盖 Bug 无关行为，保证它是合法的 PASS_TO_PASS。
	miniVisible = "package mini\n\nimport \"testing\"\n\n" +
		"func TestAddZero(t *testing.T) {\n\tif Add(0, 0) != 0 {\n\t\tt.Fatal(\"Add 错误\")\n\t}\n}\n"
	miniHidden = "package mini\n\nimport \"testing\"\n\n" +
		"func TestAddHidden(t *testing.T) {\n\tif Add(10, 5) != 15 {\n\t\tt.Fatal(\"Add 错误\")\n\t}\n}\n"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件 %s: %v", path, err)
	}
}

func newMiniFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fixture")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/mini\n\ngo 1.26.5\n")
	writeFile(t, filepath.Join(dir, "mini.go"), miniCorrect)
	writeFile(t, filepath.Join(dir, "mini_test.go"), miniVisible)
	return dir
}

// buildMiniTask 生成一道完整的迷你题目目录。
func buildMiniTask(t *testing.T, fixture string) string {
	t.Helper()
	taskDir := filepath.Join(t.TempDir(), "demo-001")

	seed, err := workspace.Materialize(fixture, filepath.Join(t.TempDir(), "seed"))
	if err != nil {
		t.Fatalf("种子仓库: %v", err)
	}
	writeFile(t, filepath.Join(seed.Dir, "mini.go"), miniBuggy)
	bugPatch, err := seed.ExtractPatch()
	if err != nil {
		t.Fatalf("生成 bug.patch: %v", err)
	}
	writeFile(t, filepath.Join(taskDir, "bug.patch"), bugPatch)

	fixed, err := workspace.Materialize(fixture,
		filepath.Join(t.TempDir(), "fixed"), filepath.Join(taskDir, "bug.patch"))
	if err != nil {
		t.Fatalf("buggy 基线: %v", err)
	}
	writeFile(t, filepath.Join(fixed.Dir, "mini.go"), miniCorrect)
	goldPatch, err := fixed.ExtractPatch()
	if err != nil {
		t.Fatalf("生成 gold.patch: %v", err)
	}
	writeFile(t, filepath.Join(taskDir, "gold.patch"), goldPatch)

	hidden, err := workspace.Materialize(fixture,
		filepath.Join(t.TempDir(), "hidden"), filepath.Join(taskDir, "bug.patch"))
	if err != nil {
		t.Fatalf("buggy 基线: %v", err)
	}
	writeFile(t, filepath.Join(hidden.Dir, "hidden_test.go"), miniHidden)
	hiddenPatch, err := hidden.ExtractPatch()
	if err != nil {
		t.Fatalf("生成 test.patch: %v", err)
	}
	writeFile(t, filepath.Join(taskDir, "test.patch"), hiddenPatch)
	return taskDir
}

func miniTask(taskDir string) task.Task {
	return task.Task{
		ID: "demo-001", Issue: "修复 Add",
		FailToPass: []string{"TestAddHidden"}, PassToPass: []string{"TestAddZero"},
		ProtectedTests: []string{"mini_test.go"},
		Dir:            taskDir, TestCommand: "go test -count=1 -json ./...", TimeoutSeconds: 120,
	}
}

// TestRunRejectsEmptyPatch 验证空补丁不会误判通过。
func TestRunRejectsEmptyPatch(t *testing.T) {
	fixture := newMiniFixture(t)
	taskDir := buildMiniTask(t, fixture)
	result, err := Run(miniTask(taskDir), "", Options{
		FixtureDir: fixture, WorkRoot: t.TempDir(),
		BuildTimeout: 60 * time.Second, TestTimeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Resolved || result.Reason != "no_changes" {
		t.Fatalf("result = %+v", result)
	}
}

// TestRunResolvesGoldAndIgnoresTampering 验证 gold 通过，且篡改可见测试、
// 新增坏测试文件都无法逃过判分。
func TestRunResolvesGoldAndIgnoresTampering(t *testing.T) {
	fixture := newMiniFixture(t)
	taskDir := buildMiniTask(t, fixture)
	item := miniTask(taskDir)
	goldData, err := os.ReadFile(item.GoldPatch())
	if err != nil {
		t.Fatalf("读 gold: %v", err)
	}
	gold := string(goldData)

	// 构造篡改补丁：修复代码 + 把可见测试改为 Skip + 新增一个无法编译的测试文件。
	tamperRepo, err := workspace.Materialize(fixture,
		filepath.Join(t.TempDir(), "tamper"), item.BugPatch())
	if err != nil {
		t.Fatalf("篡改仓库: %v", err)
	}
	writeFile(t, filepath.Join(tamperRepo.Dir, "mini.go"), miniCorrect)
	writeFile(t, filepath.Join(tamperRepo.Dir, "mini_test.go"),
		"package mini\n\nimport \"testing\"\n\nfunc TestAddZero(t *testing.T) { t.Skip(\"tampered\") }\n")
	writeFile(t, filepath.Join(tamperRepo.Dir, "broken_test.go"), "package mini\n\nfunc Broken( {\n")
	tampered, err := tamperRepo.ExtractPatch()
	if err != nil {
		t.Fatalf("生成篡改补丁: %v", err)
	}

	for name, patch := range map[string]string{"gold": gold, "tampered": tampered} {
		result, runErr := Run(item, patch, Options{
			FixtureDir: fixture, WorkRoot: t.TempDir(),
			BuildTimeout: 60 * time.Second, TestTimeout: 60 * time.Second,
		})
		if runErr != nil {
			t.Fatalf("%s: Run: %v", name, runErr)
		}
		if !result.Resolved {
			t.Fatalf("%s: result = %+v", name, result)
		}
	}
}

// TestRepoBaselineSemantics 用真实题库守住出题质量：每个 F2P 必须在
// buggy 基线上失败，每个 P2P 必须通过，否则题目无效。
func TestRepoBaselineSemantics(t *testing.T) {
	tasksDir := filepath.Join("..", "..", "tasks")
	fixtureDir := filepath.Join("..", "..", "fixture")
	items, err := task.Load(tasksDir)
	if os.IsNotExist(err) {
		t.Skip("题库尚未生成")
	}
	if err != nil {
		t.Fatalf("加载题库: %v", err)
	}
	workRoot := t.TempDir()
	for _, item := range items {
		if err = item.ValidateBasic(); err != nil {
			t.Fatalf("%s: %v", item.ID, err)
		}
		repo, err := workspace.Materialize(
			fixtureDir, filepath.Join(workRoot, "base-"+item.ID), item.BugPatch())
		if err != nil {
			t.Fatalf("%s: 物化基线: %v", item.ID, err)
		}
		if err = repo.ApplyFile(item.TestPatch()); err != nil {
			t.Fatalf("%s: 应用隐藏测试: %v", item.ID, err)
		}
		output, err := runTestCommand(repo.Dir, item.TestCommand, 2*time.Minute)
		if err != nil {
			t.Fatalf("%s: 运行测试: %v", item.ID, err)
		}
		outcomes := parseTestOutput(bytes.NewReader(output))
		for _, spec := range item.FailToPass {
			if status, ok := outcomes.lookup(spec); ok && status == "pass" {
				t.Errorf("%s: F2P %s 在 buggy 基线已通过，题目无效", item.ID, spec)
			}
		}
		for _, spec := range item.PassToPass {
			if status, ok := outcomes.lookup(spec); !ok || status != "pass" {
				t.Errorf("%s: P2P %s 在 buggy 基线未通过（status=%q, ok=%v）",
					item.ID, spec, status, ok)
			}
		}
	}
}
```

- [ ] **Step 4: 实现 judge.go**

`eval/internal/judge/judge.go`：

```go
// Package judge 负责在干净基线上应用补丁并判定测试结果。
package judge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"euler/eval/internal/report"
	"euler/eval/internal/task"
	"euler/eval/internal/workspace"
)

const (
	defaultBuildTimeout = 2 * time.Minute
	defaultTestTimeout  = 5 * time.Minute
)

// Options 控制判分环境。
type Options struct {
	FixtureDir   string
	WorkRoot     string
	KeepWorkdir  bool
	BuildTimeout time.Duration
	TestTimeout  time.Duration
}

// Result 是单题判分结果。
type Result struct {
	InstanceID string
	Resolved   bool
	Reason     string
	BuildOK    bool
	FailToPass report.TestSummary
	PassToPass report.TestSummary
}

// Run 执行单题判分。
func Run(t task.Task, modelPatch string, opts Options) (Result, error) {
	result := Result{InstanceID: t.ID}
	if strings.TrimSpace(modelPatch) == "" {
		result.Reason = "no_changes"
		return result, nil
	}
	workDir := filepath.Join(opts.WorkRoot, "judge-"+t.ID)
	if err := os.RemoveAll(workDir); err != nil {
		return result, err
	}
	if !opts.KeepWorkdir {
		defer func() { _ = os.RemoveAll(workDir) }()
	}
	repo, err := workspace.Materialize(opts.FixtureDir, workDir, t.BugPatch())
	if err != nil {
		return result, err
	}
	patchFile, err := writeTempPatch(t.ID, modelPatch)
	if err != nil {
		return result, err
	}
	defer func() { _ = os.Remove(patchFile) }()
	if err = repo.ApplyFile(patchFile); err != nil {
		result.Reason = "patch_failed"
		return result, nil
	}
	for _, path := range t.ProtectedTests {
		if restoreErr := repo.Restore(path); restoreErr != nil {
			return result, restoreErr
		}
	}
	if _, err = repo.RemoveNewTestFiles(); err != nil {
		return result, err
	}
	if err = repo.ApplyFile(t.TestPatch()); err != nil {
		return result, fmt.Errorf("应用隐藏测试: %w", err)
	}
	buildOK, buildErr := runBuild(repo.Dir, timeoutOr(opts.BuildTimeout, defaultBuildTimeout))
	result.BuildOK = buildOK
	if buildErr != nil {
		return result, buildErr
	}
	if !buildOK {
		result.Reason = "build_failed"
		return result, nil
	}
	output, testErr := runTestCommand(repo.Dir, t.TestCommand,
		timeoutOr(opts.TestTimeout, defaultTestTimeout))
	if testErr != nil {
		return result, testErr
	}
	outcomes := parseTestOutput(bytes.NewReader(output))
	result.FailToPass = summarizeTests(t.FailToPass, outcomes)
	result.PassToPass = summarizeTests(t.PassToPass, outcomes)
	if len(result.FailToPass.Failed) > 0 || len(result.PassToPass.Failed) > 0 {
		result.Reason = "tests_failed"
		return result, nil
	}
	result.Resolved = true
	return result, nil
}

func timeoutOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func writeTempPatch(id, content string) (string, error) {
	file, err := os.CreateTemp("", "euleval-"+id+"-*.patch")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err = file.WriteString(content); err != nil {
		_ = file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// runBuild 运行 go build，返回是否成功。
func runBuild(dir string, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	//nolint:gosec // 命令与参数固定
	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("go build 超时: %w", ctx.Err())
		}
		return false, nil
	}
	return true, nil
}

// runTestCommand 执行题目指定的测试命令并返回 stdout。
func runTestCommand(dir, command string, timeout time.Duration) ([]byte, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("test_command 为空")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	//nolint:gosec // 命令来自题库，属于受信输入
	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("测试超时: %w", ctx.Err())
		}
		// 测试失败本身是正常结果，继续解析输出。
	}
	return stdout.Bytes(), nil
}
```

- [ ] **Step 5: 运行测试**

Run: `cd eval && go test ./internal/judge/ -v`
Expected: PASS（3 个测试）

- [ ] **Step 6: 检查点**

Run: `cd eval && gofmt -l . && go vet ./...`
Expected: 无输出。

---

### Task 8: Runner HTTP 客户端

**Files:**
- Create: `eval/internal/runner/events.go`
- Create: `eval/internal/runner/runner.go`
- Test: `eval/internal/runner/runner_test.go`

**Interfaces:**
- Consumes: `task.Task`、`workspace.Repo`、`report.Prediction`
- Produces:
  - `runner.Options{BaseURL, ProfileID, ModelBaseURL, Model, APIKeyEnv, ModelName string; RunTimeout time.Duration}`
  - `runner.NewClient(ctx context.Context, opts Options) (*Client, error)`
  - `(*Client) RunTask(ctx context.Context, t task.Task, repo *workspace.Repo) report.Prediction`
  - `runner.DiffStats(patch string) (files, added, deleted int, touchedTest bool)`（导出以便测试）

- [ ] **Step 1: 写事件与统计测试**

`eval/internal/runner/runner_test.go`：

```go
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
		fmt.Fprint(w, `{"id":"p1","model":"m1"}`)
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"s1"}`)
	})
	mux.HandleFunc("/api/v1/sessions/s1/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"run":{"id":"r1"}}`)
	})
	mux.HandleFunc("/api/v1/runs/r1/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"seq\":1,\"type\":\"approval.requested\","+
			"\"payload\":{\"approval_id\":\"a1\"}}\n\n")
		fmt.Fprint(w, "data: {\"seq\":2,\"type\":\"run.completed\",\"payload\":{}}\n\n")
	})
	mux.HandleFunc("/api/v1/approvals/", func(w http.ResponseWriter, _ *http.Request) {
		approved.Add(1)
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("/api/v1/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"run":{"status":"succeeded"},"tool_calls":[{}],"approvals":[{}]}`)
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
		fmt.Fprint(w, `{"id":"p1","model":"m1"}`)
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"s1"}`)
	})
	mux.HandleFunc("/api/v1/sessions/s1/runs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"run":{"id":"r1"}}`)
	})
	mux.HandleFunc("/api/v1/runs/r1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		<-r.Context().Done()
	})
	mux.HandleFunc("/api/v1/runs/r1/cancel", func(w http.ResponseWriter, _ *http.Request) {
		cancelled.Add(1)
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("/api/v1/runs/r1", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"run":{"status":"running"},"tool_calls":[],"approvals":[]}`)
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
```

- [ ] **Step 2: 实现 runner.go**

`eval/internal/runner/runner.go`：

```go
// Package runner 通过 HTTP 驱动 Euler 服务完成编码任务。
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"euler/eval/internal/report"
	"euler/eval/internal/task"
	"euler/eval/internal/workspace"
)

const (
	defaultRunTimeout  = 15 * time.Minute
	healthRetryEvery   = 500 * time.Millisecond
	healthRetryTotal   = 30 * time.Second
	cancelTimeout      = 10 * time.Second
	maxPatchBytes      = 2 * 1024 * 1024
	statusCompleted    = "completed"
	statusFailed       = "failed"
	statusTimeout      = "timeout"
	statusError        = "error"
	statusPatchTooBig  = "patch_too_large"
	autoApproveReason  = "euleval auto-approve"
)

// Options 是连接 Euler 服务所需的配置。
type Options struct {
	BaseURL      string
	ProfileID    string
	ModelBaseURL string
	Model        string
	APIKeyEnv    string
	ModelName    string
	RunTimeout   time.Duration
}

// Client 是 Euler API 的轻量客户端。
type Client struct {
	baseURL    string
	httpClient *http.Client
	profileID  string
	modelName  string
	runTimeout time.Duration
}

// NewClient 完成健康检查、cookie 获取与模型配置准备。
func NewClient(ctx context.Context, opts Options) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &Client{
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		httpClient: &http.Client{Jar: jar},
		profileID:  opts.ProfileID,
		modelName:  opts.ModelName,
		runTimeout: opts.RunTimeout,
	}
	if client.runTimeout <= 0 {
		client.runTimeout = defaultRunTimeout
	}
	if err = client.waitHealthy(ctx); err != nil {
		return nil, err
	}
	if err = client.bootstrap(ctx); err != nil {
		return nil, err
	}
	if client.profileID == "" {
		if err = client.createProfile(ctx, opts); err != nil {
			return nil, err
		}
	}
	if client.modelName == "" {
		client.modelName = "euler-local"
	}
	return client, nil
}

func (c *Client) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(healthRetryTotal)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
		if err != nil {
			return err
		}
		response, err := c.httpClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Euler 服务未就绪: %s", c.baseURL)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthRetryEvery):
		}
	}
}

func (c *Client) bootstrap(ctx context.Context) error {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, c.baseURL+"/api/v1/bootstrap", nil)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("获取本机 cookie: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return fmt.Errorf("获取本机 cookie 失败: HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *Client) createProfile(ctx context.Context, opts Options) error {
	if opts.ModelBaseURL == "" || opts.Model == "" || opts.APIKeyEnv == "" {
		return errors.New("需要 --profile-id 或同时提供 --model-base-url/--model/--api-key-env")
	}
	body := map[string]string{
		"name":        fmt.Sprintf("euleval-%d", time.Now().Unix()),
		"base_url":    opts.ModelBaseURL,
		"model":       opts.Model,
		"secret_kind": "environment",
		"secret_ref":  opts.APIKeyEnv,
	}
	var created struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/model-profiles", body, &created); err != nil {
		return err
	}
	c.profileID = created.ID
	if c.modelName == "" {
		c.modelName = created.Model
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, response.StatusCode,
			strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// RunTask 执行单题并返回一条 prediction。
func (c *Client) RunTask(ctx context.Context, t task.Task, repo *workspace.Repo) report.Prediction {
	pred := report.Prediction{
		InstanceID:      t.ID,
		ModelNameOrPath: c.modelName,
		Meta: report.Meta{
			Status:     statusError,
			DurationMS: 0,
		},
	}
	started := time.Now()
	defer func() { pred.Meta.DurationMS = time.Since(started).Milliseconds() }()

	var created struct {
		ID string `json:"id"`
	}
	createBody := map[string]string{"profile_id": c.profileID, "path": repo.Dir}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/sessions", createBody, &created); err != nil {
		pred.Meta.Error = err.Error()
		return pred
	}
	var startedRun struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	runBody := map[string]string{"content": t.Issue}
	if err := c.doJSON(ctx, http.MethodPost,
		"/api/v1/sessions/"+created.ID+"/runs", runBody, &startedRun); err != nil {
		pred.Meta.Error = err.Error()
		return pred
	}
	pred.Meta.RunID = startedRun.Run.ID

	runCtx, cancel := context.WithTimeout(ctx, c.runTimeout)
	defer cancel()
	terminal, metrics, err := c.follow(runCtx, startedRun.Run.ID)
	pred.Meta.ToolCalls = metrics.toolCalls
	pred.Meta.Approvals = metrics.approvals
	switch {
	case runCtx.Err() != nil:
		c.cancelRun(startedRun.Run.ID)
		pred.Meta.Status = statusTimeout
		pred.Meta.Error = "运行超时"
	case err != nil:
		pred.Meta.Status = statusFailed
		pred.Meta.Error = err.Error()
	case terminal != "run.completed":
		pred.Meta.Status = statusFailed
		pred.Meta.Error = terminal
	default:
		pred.Meta.Status = statusCompleted
	}
	patch, patchErr := repo.ExtractPatch()
	if patchErr != nil {
		pred.Meta.Status = statusError
		pred.Meta.Error = patchErr.Error()
		return pred
	}
	if len(patch) > maxPatchBytes {
		pred.Meta.Status = statusPatchTooBig
		return pred
	}
	pred.ModelPatch = patch
	files, added, deleted, touched := DiffStats(patch)
	pred.Meta.DiffFiles = files
	pred.Meta.DiffAdded = added
	pred.Meta.DiffDeleted = deleted
	pred.Meta.TouchedTestFiles = touched
	if strings.TrimSpace(patch) == "" && pred.Meta.Status == statusCompleted {
		pred.Meta.Status = statusCompleted
	}
	return pred
}

func (c *Client) cancelRun(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	_ = c.doJSON(ctx, http.MethodPost, "/api/v1/runs/"+runID+"/cancel", nil, nil)
}

func (c *Client) snapshot(ctx context.Context, runID string) (runSnapshot, error) {
	var snap runSnapshot
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/runs/"+runID, nil, &snap)
	return snap, err
}

type runSnapshot struct {
	Run struct {
		Status string `json:"status"`
	} `json:"run"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
	Approvals []json.RawMessage `json:"approvals"`
}

// DiffStats 统计补丁的文件数与增删行数。
func DiffStats(patch string) (files, added, deleted int, touchedTest bool) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files++
		case strings.HasPrefix(line, "+++ b/"):
			if strings.HasSuffix(line, "_test.go") {
				touchedTest = true
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deleted++
		}
	}
	return files, added, deleted, touchedTest
}
```

- [ ] **Step 3: 实现 events.go**

`eval/internal/runner/events.go`：

```go
package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	maxReconnect   = 3
	pollInterval   = 2 * time.Second
	eventDataPrefix = "data: "
)

type event struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type approvalPayload struct {
	ApprovalID string `json:"approval_id"`
}

type runMetrics struct {
	toolCalls int
	approvals int
}

// follow 消费事件直到终态，返回终态事件名与指标。
func (c *Client) follow(ctx context.Context, runID string) (string, runMetrics, error) {
	var metrics runMetrics
	var after int64
	for attempt := 0; attempt <= maxReconnect; attempt++ {
		terminal, last, err := c.consume(ctx, runID, after, &metrics)
		if terminal != "" {
			return terminal, metrics, nil
		}
		if ctx.Err() != nil {
			return "", metrics, ctx.Err()
		}
		after = last
		if err != nil && attempt == maxReconnect {
			terminal, pollErr := c.poll(ctx, runID, &metrics)
			return terminal, metrics, pollErr
		}
	}
	return "", metrics, fmt.Errorf("事件流异常结束")
}

// consume 读取一次 SSE 连接。
func (c *Client) consume(
	ctx context.Context, runID string, after int64, metrics *runMetrics,
) (string, int64, error) {
	url := fmt.Sprintf("%s/api/v1/runs/%s/events?after=%d", c.baseURL, runID, after)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", after, err
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", after, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", after, fmt.Errorf("订阅事件失败: HTTP %d", response.StatusCode)
	}
	reader := bufio.NewReaderSize(response.Body, 64*1024)
	last := after
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			if ctx.Err() != nil {
				return "", last, ctx.Err()
			}
			return "", last, readErr
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, eventDataPrefix) {
			continue
		}
		var item event
		if err = json.Unmarshal([]byte(strings.TrimPrefix(line, eventDataPrefix)), &item); err != nil {
			continue
		}
		if item.Seq > last {
			last = item.Seq
		}
		switch item.Type {
		case "approval.requested":
			metrics.approvals++
			var payload approvalPayload
			if err = json.Unmarshal(item.Payload, &payload); err == nil && payload.ApprovalID != "" {
				c.approve(payload.ApprovalID)
			}
		case "run.completed", "run.failed", "run.cancelled", "run.interrupted":
			snap, snapErr := c.snapshot(ctx, runID)
			if snapErr == nil {
				metrics.toolCalls = len(snap.ToolCalls)
			}
			return item.Type, last, nil
		case "tool.completed", "tool.failed":
			metrics.toolCalls++
		}
	}
}

// approve 立即批准一次工具调用审批。
func (c *Client) approve(approvalID string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	body := map[string]any{"approved": true, "reason": autoApproveReason}
	_ = c.doJSON(ctx, http.MethodPost,
		"/api/v1/approvals/"+approvalID+"/decision", body, nil)
}

// poll 是 SSE 失败后的兜底轮询。
func (c *Client) poll(ctx context.Context, runID string, metrics *runMetrics) (string, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		snap, err := c.snapshot(ctx, runID)
		if err != nil {
			return "", err
		}
		metrics.toolCalls = len(snap.ToolCalls)
		metrics.approvals = len(snap.Approvals)
		switch snap.Run.Status {
		case "succeeded":
			return "run.completed", nil
		case "failed":
			return "run.failed", nil
		case "cancelled":
			return "run.cancelled", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
```

- [ ] **Step 4: 运行测试与构建**

Run: `cd eval && go test ./internal/runner/ -v && go build ./...`
Expected: PASS，构建成功。

- [ ] **Step 5: 检查点**

Run: `cd eval && gofmt -l . && go vet ./...`
Expected: 无输出。

---

### Task 9: CLI 装配 run / judge / smoke 与工程门禁

**Files:**
- Modify: `eval/cmd/euleval/main.go`
- Create: `eval/Makefile`
- Create: `eval/.golangci.yml`
- Test: `eval/cmd/euleval/main_test.go`

**Interfaces:**
- Consumes: 前八个任务的全部导出符号
- Produces: 可执行的四个子命令

- [ ] **Step 1: 写 CLI 参数解析测试**

`eval/cmd/euleval/main_test.go`：

```go
package main

import "testing"

func TestSplitIDs(t *testing.T) {
	got := splitIDs(" a, b ,,c ")
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("splitIDs = %v", got)
	}
	if splitIDs("  ") != nil {
		t.Fatal("空白输入应返回 nil")
	}
}
```

- [ ] **Step 2: 扩展 main.go**

在既有 `main.go` 上追加 `run`、`judge`、`smoke` 分支与实现：

```go
// runRun 执行推理阶段，逐题追加 preds.jsonl。
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	baseURL := fs.String("base-url", "http://127.0.0.1:8080", "Euler 服务地址")
	tasksDir := fs.String("tasks", "./tasks", "题库目录")
	fixtureDir := fs.String("fixture", "./fixture", "fixture 仓库目录")
	outDir := fs.String("out", "./results/run", "输出目录")
	profileID := fs.String("profile-id", "", "复用的模型配置 ID")
	modelBaseURL := fs.String("model-base-url", "", "模型 base_url")
	modelName := fs.String("model", "", "模型名")
	apiKeyEnv := fs.String("api-key-env", "", "模型 Key 所在环境变量名")
	label := fs.String("model-name", "", "写入 preds 的被测版本名")
	only := fs.String("only", "", "只跑指定题目，逗号分隔")
	runTimeout := fs.Duration("run-timeout", 15*time.Minute, "单题超时")
	workDir := fs.String("work-dir", "", "工作区根目录，缺省为临时目录")
	keepWorkdir := fs.Bool("keep-workdir", false, "保留工作区")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *apiKeyEnv != "" {
		fmt.Fprintf(os.Stderr, "提示: environment 类型密钥读取 Euler 服务进程的环境变量，请确认该进程已设置 %s\n", *apiKeyEnv)
	}
	items, err := loadSelected(*tasksDir, *only)
	if err != nil {
		return err
	}
	client, err := runner.NewClient(context.Background(), runner.Options{
		BaseURL: *baseURL, ProfileID: *profileID, ModelBaseURL: *modelBaseURL,
		Model: *modelName, APIKeyEnv: *apiKeyEnv, ModelName: *label,
		RunTimeout: *runTimeout,
	})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	workRoot := *workDir
	if workRoot == "" {
		workRoot, err = os.MkdirTemp("", "euleval-run-*")
		if err != nil {
			return err
		}
		if !*keepWorkdir {
			defer func() { _ = os.RemoveAll(workRoot) }()
		}
	}
	predsPath := filepath.Join(*outDir, "preds.jsonl")
	if err = os.Remove(predsPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, item := range items {
		if err = item.ValidateBasic(); err != nil {
			return err
		}
		dst := filepath.Join(workRoot, item.ID)
		repo, materializeErr := workspace.Materialize(*fixtureDir, dst, item.BugPatch())
		if materializeErr != nil {
			return materializeErr
		}
		pred := client.RunTask(context.Background(), item, repo)
		if err = report.AppendPrediction(predsPath, pred); err != nil {
			return err
		}
		fmt.Printf("完成 %-20s status=%s\n", item.ID, pred.Meta.Status)
		if !*keepWorkdir {
			_ = os.RemoveAll(dst)
		}
	}
	fmt.Printf("推理完成，preds 写入 %s\n", predsPath)
	return nil
}

// runJudge 执行判分阶段。
func runJudge(args []string) error {
	fs := flag.NewFlagSet("judge", flag.ContinueOnError)
	tasksDir := fs.String("tasks", "./tasks", "题库目录")
	fixtureDir := fs.String("fixture", "./fixture", "fixture 仓库目录")
	predsPath := fs.String("preds", "", "preds.jsonl 路径")
	outDir := fs.String("out", "", "输出目录")
	keepWorkdir := fs.Bool("keep-workdir", false, "保留判分工作区")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *predsPath == "" {
		return fmt.Errorf("必须提供 --preds")
	}
	if *outDir == "" {
		*outDir = filepath.Dir(*predsPath)
	}
	items, err := task.Load(*tasksDir)
	if err != nil {
		return err
	}
	byID := make(map[string]task.Task, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	preds, err := report.ReadPredictions(*predsPath)
	if err != nil {
		return err
	}
	workRoot, err := os.MkdirTemp("", "euleval-judge-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workRoot) }()
	value := report.Report{StartedAt: time.Now().Format(time.RFC3339)}
	results := make([]report.TaskResult, 0, len(preds))
	for _, pred := range preds {
		item, ok := byID[pred.InstanceID]
		if !ok {
			return fmt.Errorf("preds 中存在未知题目: %s", pred.InstanceID)
		}
		var judged judge.Result
		if strings.TrimSpace(pred.ModelPatch) == "" {
			judged = judge.Result{InstanceID: item.ID, Reason: "no_changes"}
		} else {
			judged, err = judge.Run(item, pred.ModelPatch, judge.Options{
				FixtureDir: *fixtureDir, WorkRoot: workRoot, KeepWorkdir: *keepWorkdir,
			})
			if err != nil {
				return fmt.Errorf("判分 %s: %w", item.ID, err)
			}
		}
		results = append(results, report.TaskResult{
			InstanceID: judged.InstanceID, Resolved: judged.Resolved,
			Reason: judged.Reason, BuildOK: judged.BuildOK,
			FailToPass: judged.FailToPass, PassToPass: judged.PassToPass,
			Metrics: report.Metrics{
				DurationMS: pred.Meta.DurationMS,
				ToolCalls:  pred.Meta.ToolCalls,
				DiffFiles:  pred.Meta.DiffFiles,
			},
		})
	}
	value.Tasks = results
	value.Totals = report.Summarize(results, preds)
	value.FinishedAt = time.Now().Format(time.RFC3339)
	report.PrintSummary(os.Stdout, value)
	reportPath := filepath.Join(*outDir, "report.json")
	if err = report.WriteReport(reportPath, value); err != nil {
		return err
	}
	fmt.Printf("报告写入 %s\n", reportPath)
	return nil
}

// runSmoke 用 gold 补丁验证判分器本身。
func runSmoke(args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	tasksDir := fs.String("tasks", "./tasks", "题库目录")
	fixtureDir := fs.String("fixture", "./fixture", "fixture 仓库目录")
	outDir := fs.String("out", "./results/smoke", "输出目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := task.Load(*tasksDir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	workRoot, err := os.MkdirTemp("", "euleval-smoke-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workRoot) }()
	value := report.Report{StartedAt: time.Now().Format(time.RFC3339)}
	results := make([]report.TaskResult, 0, len(items))
	preds := make([]report.Prediction, 0, len(items))
	for _, item := range items {
		data, readErr := os.ReadFile(item.GoldPatch())
		if readErr != nil {
			return readErr
		}
		pred := report.Prediction{
			InstanceID: item.ID, ModelNameOrPath: "gold",
			ModelPatch: string(data),
			Meta:       report.Meta{Status: "completed"},
		}
		preds = append(preds, pred)
		judged, judgeErr := judge.Run(item, pred.ModelPatch, judge.Options{
			FixtureDir: *fixtureDir, WorkRoot: workRoot,
		})
		if judgeErr != nil {
			return fmt.Errorf("smoke %s: %w", item.ID, judgeErr)
		}
		results = append(results, report.TaskResult{
			InstanceID: judged.InstanceID, Resolved: judged.Resolved,
			Reason: judged.Reason, BuildOK: judged.BuildOK,
			FailToPass: judged.FailToPass, PassToPass: judged.PassToPass,
		})
	}
	value.Tasks = results
	value.Totals = report.Summarize(results, preds)
	value.FinishedAt = time.Now().Format(time.RFC3339)
	report.PrintSummary(os.Stdout, value)
	if err = report.WriteReport(filepath.Join(*outDir, "report.json"), value); err != nil {
		return err
	}
	if value.Totals.Resolved != value.Totals.Tasks {
		return fmt.Errorf("smoke 未全部通过: %d/%d", value.Totals.Resolved, value.Totals.Tasks)
	}
	return nil
}
```

同时把 `main` 的 switch 补全：

```go
	switch os.Args[1] {
	case "tasks":
		err = runTasks(os.Args[2:])
	case "run":
		err = runRun(os.Args[2:])
	case "judge":
		err = runJudge(os.Args[2:])
	case "smoke":
		err = runSmoke(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
```

补充 import：`context`、`path/filepath`、`time`、`euler/eval/internal/judge`、`euler/eval/internal/report`、`euler/eval/internal/runner`、`euler/eval/internal/workspace`，以及辅助函数：

```go
func loadSelected(tasksDir, only string) ([]task.Task, error) {
	items, err := task.Load(tasksDir)
	if err != nil {
		return nil, err
	}
	return task.Filter(items, splitIDs(only))
}
```

- [ ] **Step 3: 写 Makefile**

`eval/Makefile`：

```makefile
.PHONY: test lint smoke fmt

test:
	go test ./...

lint:
	golangci-lint run --timeout=5m ./...

fmt:
	gofmt -w .

smoke:
	go run ./cmd/euleval smoke --tasks ./tasks --fixture ./fixture --out ./results/smoke
```

- [ ] **Step 4: 写 eval/.golangci.yml**

```yaml
# euleval — 独立 module 的 golangci-lint 配置。
version: "2"

run:
  timeout: 5m
  allow-parallel-runners: true
  issues-exit-code: 1

linters:
  default: standard
  enable:
    - bodyclose
    - errorlint
    - gosec
    - revive
    - lll
    - misspell
    - nolintlint
    - usestdlibvars

  settings:
    lll:
      line-length: 100
    revive:
      rules:
        - name: exported
          arguments:
            - disableStutteringCheck
        - name: package-comments
          disabled: true

  exclusions:
    rules:
      # 测试里大量内联 JSON 与断言，行宽不宜强制。
      - path: _test\.go
        linters:
          - lll
          - gosec

formatters:
  enable:
    - gofmt
    - goimports
```

- [ ] **Step 5: 运行单测与 lint**

Run: `cd eval && go test ./... && golangci-lint run --timeout=5m ./...`
Expected: 测试全绿；lint 零 issue。若 `lll` 报行宽超限，按提示换行，不得用 `//nolint` 掩盖真实问题。

- [ ] **Step 6: 检查点**

Run: `cd eval && gofmt -l . && go build ./...`
Expected: 无输出，构建成功。

---

### Task 10: 端到端验收

**Files:**
- 不新增文件；只验证。

- [ ] **Step 1: 全量单测**

Run: `cd eval && go test ./... -count=1`
Expected: 所有包 PASS。

- [ ] **Step 2: lint 门禁**

Run: `cd eval && golangci-lint run --timeout=5m ./...`
Expected: `0 issues.`

- [ ] **Step 3: 题库校验**

Run: `cd eval && go run ./cmd/euleval tasks validate --tasks ./tasks --fixture ./fixture`
Expected: 5 行 `OK`。

- [ ] **Step 4: smoke 判分器自检**

Run: `cd eval && go run ./cmd/euleval smoke --tasks ./tasks --fixture ./fixture --out ./results/smoke`
Expected: 5 行 `PASS`，`总计: 5/5 resolved (100.0%)`，退出码 0。

- [ ] **Step 5: 空补丁回归**

在临时目录构造一行 `preds.jsonl`：

```bash
printf '%s\n' '{"instance_id":"textutil-001","model_name_or_path":"empty","model_patch":"","meta":{"status":"completed"}}' > /tmp/euleval-empty/preds.jsonl
```

Run: `cd eval && go run ./cmd/euleval judge --tasks ./tasks --fixture ./fixture --preds /tmp/euleval-empty/preds.jsonl --out /tmp/euleval-empty`
Expected: `FAIL  textutil-001 ... no_changes`，总计 0/1，退出码 0。

- [ ] **Step 6: 真机联调（可选，需要 PostgreSQL 与模型 Key）**

```bash
# 终端 1
docker compose up -d postgres
export EULER_DATABASE_URL='postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable'
export EVAL_MODEL_KEY='<你的 key>'
go run ./cmd/euler

# 终端 2
cd eval
go run ./cmd/euleval run --base-url http://127.0.0.1:8080 \
  --tasks ./tasks --fixture ./fixture --out ./results/run-1 \
  --model-base-url https://api.example/v1 --model <模型名> \
  --api-key-env EVAL_MODEL_KEY --model-name euler-local --only textutil-001
go run ./cmd/euleval judge --tasks ./tasks --fixture ./fixture \
  --preds ./results/run-1/preds.jsonl --out ./results/run-1
```

Expected: `preds.jsonl` 含 1 条记录；`report.json` 产生；若 Agent 未修复，`resolved=false` 且 `fail_to_pass.failed` 非空。

- [ ] **Step 7: 最终检查点**

Run: `cd eval && git status --short`
Expected: 仅包含源码与题库文件；`results/` 被 `.gitignore` 忽略。
