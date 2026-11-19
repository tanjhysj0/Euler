package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	err := task.WithDefaults().ValidateBasic()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("F2P 与 P2P 重叠必须返回 ErrInvalid，实际: %v", err)
	}
}

func TestFilterReportsMissingID(t *testing.T) {
	_, err := Filter([]Task{{ID: "a"}}, []string{"a", "b"})
	if err == nil {
		t.Fatal("缺失题目 ID 必须报错")
	}
}

func TestLoadRejectsDuplicateID(t *testing.T) {
	root := t.TempDir()
	body := `{
		"id": "demo-001",
		"title": "示例",
		"issue": "修复示例",
		"fail_to_pass": ["TestA"],
		"pass_to_pass": ["TestB"]
	}`
	writeTask(t, root, "demo-001", body)
	writeTask(t, root, "demo-001-copy", body)
	if _, err := Load(root); !errors.Is(err, ErrInvalid) {
		t.Fatalf("重复 id 必须返回 ErrInvalid，实际: %v", err)
	}
}

func TestLoadReturnsAbsoluteDir(t *testing.T) {
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
	if !filepath.IsAbs(tasks[0].Dir) {
		t.Fatalf("Dir 不是绝对路径: %s", tasks[0].Dir)
	}
	if !strings.HasPrefix(tasks[0].BugPatch(), tasks[0].Dir) {
		t.Fatalf("BugPatch 未以 Dir 开头: %s", tasks[0].BugPatch())
	}
}
