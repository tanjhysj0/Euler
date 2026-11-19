package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	// writeDirPermission 与工作区根目录的私有 0700 权限保持一致，
	// 使写入过程中创建的目录对其他用户不可读。
	writeDirPermission = 0o700
	// tempFilePrefix 标记正在进行中的写入；临时文件本身已使用 0600 权限。
	tempFilePrefix = ".euler-write-"
	// diffContextLines 让 unified diff 保持紧凑但仍便于审阅。
	diffContextLines = 3
	// emptyString 是补丁校验时的零长度字节切片边界。
	emptyString = ""
	// firstValidIndex 是 bytes.Index 匹配成功时可能返回的最小下标。
	firstValidIndex = 0
)

var (
	// ErrPatchAmbiguous 表示旧字符串并非恰好出现一次。
	ErrPatchAmbiguous = errors.New("old string must appear exactly once")
	// ErrNotDeletable 表示删除目标不是普通文件。
	ErrNotDeletable = errors.New("workspace path is not a deletable file")
	// errDeleteStillPresent 用于防范删除操作悄无声息地未生效。
	errDeleteStillPresent = errors.New("verify deletion: path still exists")
)

// PrepareWrite 计算将 content 写入 requested 的 diff，
// 此时尚不触碰文件系统。
func (s *FileStore) PrepareWrite(
	_ context.Context, requested string, content []byte,
) (FileChange, error) {
	joined, err := s.resolve(requested)
	if err != nil {
		return FileChange{}, err
	}
	if err = s.checkWriteTarget(joined); err != nil {
		return FileChange{}, err
	}
	existing, existingErr := s.readExisting(requested)
	if existingErr != nil && !errors.Is(existingErr, ErrNotFound) {
		return FileChange{}, existingErr
	}
	diff, err := renderDiff(requested, existing, content)
	if err != nil {
		return FileChange{}, err
	}
	return FileChange{Diff: diff, Apply: func(_ context.Context) error {
		return s.applyWrite(requested, content)
	}}, nil
}

// PreparePatch 计算将 requested 中唯一出现的 oldString 替换为 newString
// 的 diff，不触碰文件系统。
func (s *FileStore) PreparePatch(
	_ context.Context, requested, oldString, newString string,
) (FileChange, error) {
	updated, err := s.patchedContent(requested, oldString, newString)
	if err != nil {
		return FileChange{}, err
	}
	existing, existingErr := s.readExisting(requested)
	if existingErr != nil {
		return FileChange{}, existingErr
	}
	diff, err := renderDiff(requested, existing, updated)
	if err != nil {
		return FileChange{}, err
	}
	return FileChange{Diff: diff, Apply: func(_ context.Context) error {
		return s.applyPatch(requested, oldString, newString)
	}}, nil
}

// Delete 计算一个删除 requested 的 FileChange。只有工作区内的
// 普通文件才能被删除。
func (s *FileStore) Delete(_ context.Context, requested string) (FileChange, error) {
	if err := s.regularTarget(requested); err != nil {
		return FileChange{}, err
	}
	return FileChange{
		Diff: "deleted: " + requested + "\n",
		Apply: func(_ context.Context) error {
			return s.applyDelete(requested)
		},
	}, nil
}

// checkWriteTarget 拒绝 symlink 或非常规文件的写入目标。指向工作区之外的
// symlink 目标会以 ErrOutsideWorkspace 报错，因此被调换的链接
// 永远无法把写入重定向到别处。
func (s *FileStore) checkWriteTarget(joined string) error {
	info, err := os.Lstat(joined)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect workspace path: %w", err)
	}
	if info.Mode()&os.ModeSymlink == os.ModeSymlink {
		if _, resolveErr := s.resolveExisting(joined); resolveErr != nil {
			return resolveErr
		}
		return ErrNotRegularFile
	}
	if !info.Mode().IsRegular() {
		return ErrNotRegularFile
	}
	return nil
}

// applyWrite 重新校验 requested，并通过临时文件加 rename 的方式
// 以 0600 权限写入 content。
func (s *FileStore) applyWrite(requested string, content []byte) error {
	joined, err := s.resolve(requested)
	if err != nil {
		return err
	}
	if err = s.checkWriteTarget(joined); err != nil {
		return err
	}
	if err = s.ensureWritableDir(joined); err != nil {
		return err
	}
	return writeFileAtomic(joined, content)
}

// ensureWritableDir 校验最近的已存在祖先目录仍位于工作区内，
// 并创建缺失的父目录。
func (s *FileStore) ensureWritableDir(joined string) error {
	dir := filepath.Dir(joined)
	if err := s.validateAncestor(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, writeDirPermission); err != nil {
		return fmt.Errorf("create workspace directories: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return mapResolveError(err)
	}
	return s.ensureInside(resolved)
}

// validateAncestor 从 dir 逐级向上找到最近的已存在祖先目录，
// 并要求其 symlink 解析结果仍位于工作区内。
func (s *FileStore) validateAncestor(dir string) error {
	for dir != s.root {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return s.ensureInside(resolved)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("resolve workspace directory: %w", err)
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

// writeFileAtomic 通过临时文件加 rename 的方式把 content 写到 joined
// 所在位置，并保留临时文件的 0600 权限。
func writeFileAtomic(joined string, content []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(joined), tempFilePrefix)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempName := temp.Name()
	if _, writeErr := temp.Write(content); writeErr != nil {
		return discardTemp(temp, tempName, fmt.Errorf("write temporary file: %w", writeErr))
	}
	if err = temp.Close(); err != nil {
		return discardTemp(temp, tempName, fmt.Errorf("close temporary file: %w", err))
	}
	if err = os.Rename(tempName, joined); err != nil {
		removeErr := os.Remove(tempName)
		return errors.Join(fmt.Errorf("move file into place: %w", err), removeErr)
	}
	return nil
}

// discardTemp 关闭并删除写入失败的临时文件，同时保留原始错误原因。
func discardTemp(temp *os.File, name string, cause error) error {
	closeErr := temp.Close()
	removeErr := os.Remove(name)
	return errors.Join(cause, closeErr, removeErr)
}

// patchedContent 返回打补丁后的文件内容，要求 oldString 在当前文件中
// 恰好出现一次。
func (s *FileStore) patchedContent(requested, oldString, newString string) ([]byte, error) {
	if oldString == emptyString {
		return nil, ErrPatchAmbiguous
	}
	existing, err := s.readExisting(requested)
	if err != nil {
		return nil, err
	}
	updated, ok := replaceOnce(existing, []byte(oldString), []byte(newString))
	if !ok {
		return nil, ErrPatchAmbiguous
	}
	return updated, nil
}

// applyPatch 重新读取文件，再次确认 oldString 恰好出现一次，
// 然后原子地写入打补丁后的内容。
func (s *FileStore) applyPatch(requested, oldString, newString string) error {
	updated, err := s.patchedContent(requested, oldString, newString)
	if err != nil {
		return err
	}
	return s.applyWrite(requested, updated)
}

// regularTarget 对 requested 执行 stat 并要求其为已存在的普通文件，
// 使 symlink 逃逸在删除类型检查之前就失败。
func (s *FileStore) regularTarget(requested string) error {
	joined, err := s.resolve(requested)
	if err != nil {
		return err
	}
	// 符号链接先经过完整解析验证不逃逸，再要求链接本身是普通文件。
	if _, err = s.resolveExisting(joined); err != nil {
		return err
	}
	info, err := os.Lstat(joined)
	if err != nil {
		return mapResolveError(err)
	}
	if !info.Mode().IsRegular() {
		return ErrNotDeletable
	}
	return nil
}

// applyDelete 重新校验 requested，删除文件后再次检查该路径，
// 防止竞态导致删除悄无声息地未生效。
func (s *FileStore) applyDelete(requested string) error {
	if err := s.regularTarget(requested); err != nil {
		return err
	}
	joined, err := s.resolve(requested)
	if err != nil {
		return err
	}
	if err = os.Remove(joined); err != nil {
		return fmt.Errorf("delete workspace file: %w", err)
	}
	if _, statErr := os.Lstat(joined); statErr == nil {
		return errDeleteStillPresent
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("verify deletion: %w", statErr)
	}
	return nil
}

// replaceOnce 替换 old 的唯一一次出现，并报告 old 是否
// 恰好出现了一次。
func replaceOnce(content, old, replacement []byte) ([]byte, bool) {
	index := bytes.Index(content, old)
	if index < firstValidIndex || bytes.Contains(content[index+len(old):], old) {
		return nil, false
	}
	updated := make([]byte, emptyCapacity, len(content)+len(replacement)-len(old))
	updated = append(updated, content[:index]...)
	updated = append(updated, replacement...)
	updated = append(updated, content[index+len(old):]...)
	return updated, true
}

// renderDiff 渲染 before 与 after 内容之间的 unified diff；
// 新文件以空内容作为 diff 源。
func renderDiff(path string, before, after []byte) (string, error) {
	fromFile := "a/" + path
	if len(before) == emptyCapacity {
		fromFile = "/dev/null"
	}
	diff := difflib.UnifiedDiff{
		A:        splitDiffLines(before),
		B:        splitDiffLines(after),
		FromFile: fromFile,
		ToFile:   "b/" + path,
		Context:  diffContextLines,
	}
	buf := new(bytes.Buffer)
	if err := difflib.WriteUnifiedDiff(buf, diff); err != nil {
		return emptyString, fmt.Errorf("render diff: %w", err)
	}
	return buf.String(), nil
}

// splitDiffLines 拆分 diff 输入；空内容不产生任何行，
// 使新文件渲染为纯新增。
func splitDiffLines(content []byte) []string {
	if len(content) == emptyCapacity {
		return nil
	}
	return difflib.SplitLines(string(content))
}
