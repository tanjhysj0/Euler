// Package workspace 负责从 fixture 物化隔离的 git 工作区。
package workspace

import (
	"errors"
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
	entries, err := os.ReadDir(dstDir)
	if err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("目标目录 %s 必须为空", dstDir)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("检查目标目录 %s: %w", dstDir, err)
	}
	if err = CopyDir(fixtureDir, dstDir); err != nil {
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
	data, err := os.ReadFile(path) //nolint:gosec // 补丁路径由评测器提供
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
	// 第一阶段只收集待删除文件，避免在 WalkDir 回调内做删除操作。
	type pendingFile struct {
		abs string
		rel string
	}
	pending := make([]pendingFile, 0)
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
		pending = append(pending, pendingFile{abs: path, rel: rel})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	// 第二阶段逐个删除收集到的文件。
	for _, item := range pending {
		if err := os.Remove(item.abs); err != nil {
			return nil, err
		}
		removed = append(removed, item.rel)
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
		// 先跳过 .git：目录整体跳过，文件（worktree/submodule 指针）仅跳过自身，
		// 避免子仓库指针破坏目标工作区的隔离性。
		if entry.Name() == gitDirName {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755) //nolint:gosec // 工作区目录需可读执行
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // 来源为受信 fixture
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, data, 0o644) //nolint:gosec // 工作区文件按常规权限复制
	})
}
