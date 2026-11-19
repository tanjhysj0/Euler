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
