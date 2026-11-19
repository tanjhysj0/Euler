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
	if err := os.WriteFile(filepath.Join(dir, "hello_test.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatalf("写 fixture 测试文件: %v", err)
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
	if _, err = os.Stat(filepath.Join(repo.Dir, "hello_test.go")); err != nil {
		t.Fatalf("基线测试文件应保留: %v", err)
	}
}

func TestRestoreResetsTamperedFile(t *testing.T) {
	fixture := newFixture(t)
	repo, err := Materialize(fixture, filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if err = os.WriteFile(filepath.Join(repo.Dir, "hello.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("篡改文件: %v", err)
	}
	if err = repo.Restore("hello.txt"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repo.Dir, "hello.txt"))
	if err != nil {
		t.Fatalf("读回文件: %v", err)
	}
	if string(got) != "v1\n" {
		t.Fatalf("文件内容 = %q", got)
	}
	status, err := outputGit(repo.Dir, "status", "--porcelain", "--", "hello.txt")
	if err != nil {
		t.Fatalf("查看状态: %v", err)
	}
	if strings.TrimSpace(status) != "" {
		t.Fatalf("还原后仍有改动:\n%s", status)
	}
}

func TestMaterializeRejectsNonEmptyDir(t *testing.T) {
	fixture := newFixture(t)
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "leftover.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("写残留文件: %v", err)
	}
	if _, err := Materialize(fixture, dst); err == nil {
		t.Fatal("非空目标目录必须报错")
	}
}

func TestMaterializeSkipsGitFile(t *testing.T) {
	fixture := newFixture(t)
	if err := os.WriteFile(filepath.Join(fixture, ".git"),
		[]byte("gitdir: /tmp/nonexistent\n"), 0o644); err != nil {
		t.Fatalf("写 .git 文件: %v", err)
	}
	repo, err := Materialize(fixture, filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	info, err := os.Stat(filepath.Join(repo.Dir, ".git"))
	if err != nil {
		t.Fatalf("stat .git: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("目标 .git 应为目录，而非复制的 .git 文件")
	}
	if _, err = os.Stat(filepath.Join(repo.Dir, "hello.txt")); err != nil {
		t.Fatalf("hello.txt 应被复制: %v", err)
	}
	top, err := outputGit(repo.Dir, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	want, err := filepath.EvalSymlinks(repo.Dir)
	if err != nil {
		t.Fatalf("解析目标路径: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		t.Fatalf("解析仓库根: %v", err)
	}
	if got != want {
		t.Fatalf("仓库根 = %q, 期望 %q", got, want)
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
