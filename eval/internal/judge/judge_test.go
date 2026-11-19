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
