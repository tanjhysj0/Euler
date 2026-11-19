package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// preparePatches 用 workspace 无关的方式写出一个最小 fixture 与两道补丁。
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
	err := ValidatePatches(value, fixture, t.TempDir())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("缺失补丁必须返回 ErrInvalid，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "gold.patch") {
		t.Fatalf("错误信息应包含 gold.patch，实际: %v", err)
	}
}

func TestValidatePatchesSuccess(t *testing.T) {
	dir := t.TempDir()
	fixture := preparePatches(t, dir)
	value := Task{
		ID: "demo-001", Issue: "x",
		FailToPass: []string{"TestA"}, PassToPass: []string{"TestB"},
		Dir: filepath.Join(dir, "tasks", "demo-001"),
	}.WithDefaults()
	workRoot := t.TempDir()
	if err := ValidatePatches(value, fixture, workRoot); err != nil {
		t.Fatalf("合法补丁必须通过: %v", err)
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		t.Fatalf("读取工作根目录: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("校验后不应有残留目录，实际 %d 项", len(entries))
	}
}
