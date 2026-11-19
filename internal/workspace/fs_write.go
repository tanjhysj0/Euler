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
	// writeDirPermission matches the private 0700 mode of workspace roots so
	// directories created while writing stay unreadable by other users.
	writeDirPermission = 0o700
	// tempFilePrefix marks in-progress writes; temp files already use 0600.
	tempFilePrefix = ".euler-write-"
	// diffContextLines keeps unified diffs compact but reviewable.
	diffContextLines = 3
	// emptyString is the zero byte slice bound for patch validation.
	emptyString = ""
	// firstValidIndex is the smallest index bytes.Index can report for a match.
	firstValidIndex = 0
)

var (
	// ErrPatchAmbiguous marks old strings that do not occur exactly once.
	ErrPatchAmbiguous = errors.New("old string must appear exactly once")
	// ErrNotDeletable marks delete targets that are not regular files.
	ErrNotDeletable = errors.New("workspace path is not a deletable file")
	// errDeleteStillPresent guards against deletes that silently no-op.
	errDeleteStillPresent = errors.New("verify deletion: path still exists")
)

// PrepareWrite computes the diff of writing content to requested without
// touching the filesystem yet.
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

// PreparePatch computes the diff of replacing the single occurrence of
// oldString in requested with newString without touching the filesystem.
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

// Delete computes a FileChange removing requested. Only regular files inside
// the workspace can be deleted.
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

// checkWriteTarget rejects symlinked or non-regular write targets. Symlink
// targets pointing outside the workspace surface as ErrOutsideWorkspace so a
// swapped link can never redirect a write.
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

// applyWrite re-validates requested and writes content with a temporary file
// plus rename at 0600 permissions.
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

// ensureWritableDir validates the nearest existing ancestor stays inside the
// workspace and creates missing parent directories.
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

// validateAncestor walks up from dir to the nearest existing ancestor and
// requires its symlink resolution to stay inside the workspace.
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

// writeFileAtomic writes content next to joined using a temporary file and
// rename, keeping 0600 permissions from the temporary file.
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

// discardTemp closes and removes a failed temporary file, keeping the cause.
func discardTemp(temp *os.File, name string, cause error) error {
	closeErr := temp.Close()
	removeErr := os.Remove(name)
	return errors.Join(cause, closeErr, removeErr)
}

// patchedContent returns the patched file contents, requiring oldString to
// occur exactly once in the current file.
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

// applyPatch re-reads the file, re-verifies oldString occurs exactly once and
// writes the patched content atomically.
func (s *FileStore) applyPatch(requested, oldString, newString string) error {
	updated, err := s.patchedContent(requested, oldString, newString)
	if err != nil {
		return err
	}
	return s.applyWrite(requested, updated)
}

// regularTarget stats requested and requires an existing regular file, so
// symlink escapes fail before delete type checks.
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

// applyDelete re-validates requested, removes the file and re-checks the
// path so races cannot silently no-op.
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

// replaceOnce replaces a single occurrence of old and reports whether old
// occurred exactly once.
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

// renderDiff renders a unified diff between before and after contents; new
// files diff from an empty source.
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

// splitDiffLines splits diff input; empty content yields no lines so new
// files render as pure additions.
func splitDiffLines(content []byte) []string {
	if len(content) == emptyCapacity {
		return nil
	}
	return difflib.SplitLines(string(content))
}
