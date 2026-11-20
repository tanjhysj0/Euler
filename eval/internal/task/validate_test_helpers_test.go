package task

import (
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
