// Package workspace 校验工作区目录，并在不读取文件内容的前提下
// 浏览本地目录。
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

const (
	maxDirectories = 200
	dirPermission  = 0o700
	emptyPath      = ""
	emptyLength    = 0
)

var (
	// ErrInvalidPath 表示路径不是可访问的绝对目录。
	ErrInvalidPath = errors.New("invalid path")
	// ErrUnavailable 表示工作区根目录无法创建或不可写。
	ErrUnavailable = errors.New("workspace unavailable")
)

// Service 为会话解析工作区目录。
type Service struct {
	root string
}

// NewService 构建一个在 root 下创建默认工作区的 Service。
func NewService(root string) *Service {
	return &Service{root: root}
}

// NormalizeExisting 将 path 解析为本进程可读写的规范绝对目录。
// 各项检查之前会先解析 symlink。
func (*Service) NormalizeExisting(path string) (string, error) {
	if path == emptyPath || !filepath.IsAbs(path) {
		return emptyPath, fmt.Errorf("%w: path must be absolute", ErrInvalidPath)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return emptyPath, wrapPathError(err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return emptyPath, wrapPathError(err)
	}
	if !info.IsDir() {
		return emptyPath, fmt.Errorf("%w: path is not a directory", ErrInvalidPath)
	}
	if err = checkAccessible(resolved); err != nil {
		return emptyPath, err
	}
	return resolved, nil
}

// EnsureRoot 在启动阶段创建并探测工作区根目录。
// 提前暴露权限问题，避免用户创建会话时才收到笼统的失败提示。
func (s *Service) EnsureRoot() error {
	root, err := filepath.Abs(s.root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	if err = os.MkdirAll(root, dirPermission); err != nil {
		return fmt.Errorf("%w: create workspace root %s: %w", ErrUnavailable, root, err)
	}
	probe, err := os.CreateTemp(root, ".euler-write-probe-*")
	if err != nil {
		return fmt.Errorf("%w: workspace root %s is not writable: %w", ErrUnavailable, root, err)
	}
	name := probe.Name()
	if err = errors.Join(probe.Close(), os.Remove(name)); err != nil {
		return fmt.Errorf("%w: clean workspace probe: %w", ErrUnavailable, err)
	}
	return nil
}

// CreateDefault 在服务根目录下创建以 sessionID 命名的 0700 目录，
// 并返回其规范化的绝对路径。
func (s *Service) CreateDefault(sessionID uuid.UUID) (string, bool, error) {
	root, err := filepath.Abs(s.root)
	if err != nil {
		return emptyPath, false, fmt.Errorf("resolve workspace root: %w", err)
	}
	if err = os.MkdirAll(root, dirPermission); err != nil {
		return emptyPath, false,
			fmt.Errorf("%w: create workspace root: %w", ErrUnavailable, err)
	}
	target := filepath.Join(root, sessionID.String())
	if err = os.Mkdir(target, dirPermission); err != nil {
		return emptyPath, false,
			fmt.Errorf("%w: create session workspace: %w", ErrUnavailable, err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return emptyPath, false,
			fmt.Errorf("%w: %w", ErrUnavailable, removeAfterFailure(target, err))
	}
	return resolved, true, nil
}

// CleanupCreated 删除由 CreateDefault 创建的目录。它只对本次创建的
// 路径执行且绝不递归删除，因此不会触碰用户数据。
func (*Service) CleanupCreated(path string, created bool) error {
	switch created {
	case true:
		return removeEmptyDirectory(path)
	case false:
		return nil
	}
	return nil
}

func removeEmptyDirectory(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove workspace: %w", err)
	}
	return nil
}

// Directory 表示目录列表中的一个具名子目录。
type Directory struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Listing 描述一个可浏览目录及其直接子目录。
type Listing struct {
	CurrentPath string      `json:"current_path"`
	ParentPath  *string     `json:"parent_path"`
	Directories []Directory `json:"directories"`
}

// Browse 在不读取文件内容的前提下列出 path 的直接子目录。
// 空路径默认使用用户主目录。
func Browse(path string) (Listing, error) {
	current, err := resolveBrowsePath(path)
	if err != nil {
		return Listing{}, err
	}
	entries, err := os.ReadDir(current)
	if err != nil {
		return Listing{}, wrapPathError(err)
	}
	return Listing{
		CurrentPath: current,
		ParentPath:  parentPath(current),
		Directories: collectDirectories(current, entries),
	}, nil
}

func resolveBrowsePath(path string) (string, error) {
	if path == emptyPath {
		home, err := os.UserHomeDir()
		if err != nil {
			return emptyPath, fmt.Errorf("resolve home directory: %w", err)
		}
		path = home
	}
	if !filepath.IsAbs(path) {
		return emptyPath, fmt.Errorf("%w: path must be absolute", ErrInvalidPath)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return emptyPath, wrapPathError(err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return emptyPath, wrapPathError(err)
	}
	if !info.IsDir() {
		return emptyPath, fmt.Errorf("%w: path is not a directory", ErrInvalidPath)
	}
	return resolved, nil
}

func collectDirectories(current string, entries []os.DirEntry) []Directory {
	capacity := len(entries)
	if capacity > maxDirectories {
		capacity = maxDirectories
	}
	directories := make([]Directory, emptyLength, capacity)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if len(directories) == maxDirectories {
			break
		}
		directories = append(directories, Directory{
			Name: entry.Name(),
			Path: filepath.Join(current, entry.Name()),
		})
	}
	return directories
}

func parentPath(current string) *string {
	parent := filepath.Dir(current)
	if parent == current {
		return nil
	}
	return &parent
}

func checkAccessible(dir string) error {
	if _, err := os.ReadDir(dir); err != nil {
		return wrapPathError(err)
	}
	probe, err := os.CreateTemp(dir, ".euler-access-*")
	if err != nil {
		return wrapPathError(err)
	}
	name := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if joinErr := errors.Join(closeErr, removeErr); joinErr != nil {
		return wrapPathError(joinErr)
	}
	return nil
}

func removeAfterFailure(target string, cause error) error {
	resolveErr := fmt.Errorf("resolve session workspace: %w", cause)
	if err := os.Remove(target); err != nil {
		return errors.Join(resolveErr, err)
	}
	return resolveErr
}

func wrapPathError(err error) error {
	return fmt.Errorf("%w: %w", ErrInvalidPath, err)
}
