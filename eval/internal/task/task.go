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

	// Dir 是题目目录的绝对路径（由 Load 填充），不参与 JSON 序列化。
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
	dirByID := make(map[string]string, len(tasks))
	for _, item := range tasks {
		if prev, ok := dirByID[item.ID]; ok {
			return nil, fmt.Errorf("%w: id %s 重复: %s 与 %s", ErrInvalid, item.ID, prev, item.Dir)
		}
		dirByID[item.ID] = item.Dir
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
}

func loadOne(dir string) (Task, error) {
	// 题库目录由调用方提供，属于受信输入，此处忽略 G304 文件包含告警。
	data, err := os.ReadFile(filepath.Join(dir, taskFileName)) //nolint:gosec // 受信题库目录
	if err != nil {
		return Task{}, fmt.Errorf("读取 %s: %w", dir, err)
	}
	var value Task
	if err = json.Unmarshal(data, &value); err != nil {
		return Task{}, fmt.Errorf("%w: %s: %w", ErrInvalid, dir, err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Task{}, fmt.Errorf("解析 %s 绝对路径: %w", dir, err)
	}
	value.Dir = absDir
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
