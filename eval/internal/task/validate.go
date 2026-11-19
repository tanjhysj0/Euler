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
		return fmt.Errorf("%w: %s: bug.patch 应用失败: %w", ErrInvalid, t.ID, err)
	}
	if err = repo.ApplyFile(second); err != nil {
		return fmt.Errorf("%w: %s: 补丁 %s 应用失败: %w", ErrInvalid, t.ID, second, err)
	}
	return nil
}
