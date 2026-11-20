package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// maxListNodes 限制单个工作区返回的文件树节点数。
	maxListNodes = 1000
	// maxSearchResults 限制相对路径搜索的匹配数量。
	maxSearchResults = 200
	// emptyCapacity 用于构建带预设长度上限的空切片。
	emptyCapacity = 0
	// typeDir 和 typeFile 是工作区文件树的节点类型。
	typeDir  = "dir"
	typeFile = "file"
)

var (
	// ErrOutsideWorkspace 标记逃逸出工作区根目录的路径。
	ErrOutsideWorkspace = errors.New("path escapes workspace root")
	// ErrNotFound 标记不存在的工作区路径。
	ErrNotFound = errors.New("workspace path not found")
	// ErrTooLarge 标记超过配置读取上限的文件。
	ErrTooLarge = errors.New("file exceeds the read limit")
	// ErrNotRegularFile 标记目录及其他非普通文件的目标。
	ErrNotRegularFile = errors.New("workspace path is not a regular file")
	// ErrNotDirectory 标记目标不是目录。
	ErrNotDirectory = errors.New("workspace path is not a directory")
)

// FileNode 是工作区文件树中的一个条目。
type FileNode struct {
	Path    string    `json:"path"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// FileChange 是一个已审查但尚未应用的文件变更。Apply 在执行时会重新
// 校验路径，因此 symlink 替换无法逃逸出工作区。
type FileChange struct {
	Diff  string
	Apply func(context.Context) error
}

// FileStore 安全地读取工作区文件并预先计算已审查的变更。
// 写操作只有在 FileChange.Apply 执行时才真正落盘。
type FileStore struct {
	root   string
	inside string
	limit  int
}

// NewFileStore 为一个工作区目录构建 FileStore。limit 限制 Read 返回的
// 数据量；根目录的 symlink 解析结果会被缓存用于包含性检查，因此以
// symlink 形式存在的工作区根目录仍能接受其自身的文件。
func NewFileStore(root string, limit int) *FileStore {
	inside, err := filepath.EvalSymlinks(root)
	if err != nil {
		inside = root
	}
	return &FileStore{root: root, inside: inside, limit: limit}
}

// Read 最多返回工作区内某个普通文件的 limit 个字节。
func (s *FileStore) Read(_ context.Context, requested string) ([]byte, error) {
	file, err := s.openReadFile(requested)
	if err != nil {
		return nil, err
	}
	data, readErr := readLimited(file, s.limit)
	return data, errors.Join(readErr, file.Close())
}

// List 返回工作区文件树，节点数上限为 maxListNodes。符号链接会被
// 列出，但绝不跟进其目标。
func (s *FileStore) List(_ context.Context) ([]FileNode, error) {
	nodes := make([]FileNode, emptyCapacity, maxListNodes)
	err := s.walk(func(rel string, entry fs.DirEntry) error {
		if len(nodes) < maxListNodes {
			node, nodeErr := newFileNode(rel, entry)
			if nodeErr != nil {
				return nodeErr
			}
			nodes = append(nodes, node)
		}
		if len(nodes) >= maxListNodes {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(nodes, func(a, b FileNode) int { return strings.Compare(a.Path, b.Path) })
	return nodes, nil
}

// ListDir 返回工作区内 dir 的直接子项，不做递归；dir 为空或 "." 时
// 列出工作区根目录。符号链接会被列出，但绝不跟进其目标。
func (s *FileStore) ListDir(_ context.Context, dir string) ([]FileNode, error) {
	resolved, err := s.resolveDirectory(dir)
	if err != nil {
		return nil, err
	}
	return s.readDirNodes(resolved)
}

// resolveDirectory 解析 dir，并要求其存在且是目录。
func (s *FileStore) resolveDirectory(dir string) (string, error) {
	requested := dir
	if requested == emptyPath {
		requested = "."
	}
	joined, err := s.resolve(requested)
	if err != nil {
		return emptyPath, err
	}
	resolved, err := s.resolveExisting(joined)
	if err != nil {
		return emptyPath, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return emptyPath, mapResolveError(err)
	}
	if !info.IsDir() {
		return emptyPath, ErrNotDirectory
	}
	return resolved, nil
}

// readDirNodes 读取目录的直接子项并转换为工作区相对路径节点。
func (s *FileStore) readDirNodes(dir string) ([]FileNode, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, mapResolveError(err)
	}
	capacity := len(entries)
	if capacity > maxListNodes {
		capacity = maxListNodes
	}
	nodes := make([]FileNode, emptyCapacity, capacity)
	for _, entry := range entries {
		if len(nodes) >= maxListNodes {
			break
		}
		node, nodeErr := s.dirNode(dir, entry)
		if nodeErr != nil {
			return nil, nodeErr
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// dirNode 以工作区根为基准构建子项节点，使返回路径可直接用于其他工具。
func (s *FileStore) dirNode(dir string, entry fs.DirEntry) (FileNode, error) {
	rel, err := filepath.Rel(s.inside, filepath.Join(dir, entry.Name()))
	if err != nil {
		return FileNode{}, fmt.Errorf("resolve workspace entry: %w", err)
	}
	return newFileNode(rel, entry)
}

// Search 返回最多 maxSearchResults 个包含 query 的相对路径。
func (s *FileStore) Search(_ context.Context, query string) ([]string, error) {
	matches := make([]string, emptyCapacity, maxSearchResults)
	err := s.walk(func(rel string, _ fs.DirEntry) error {
		if len(matches) < maxSearchResults && strings.Contains(rel, query) {
			matches = append(matches, rel)
		}
		if len(matches) >= maxSearchResults {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(matches)
	return matches, nil
}

// openReadFile 解析、校验并以只读方式打开 requested。调用方必须
// 关闭返回的文件。
func (s *FileStore) openReadFile(requested string) (*os.File, error) {
	joined, err := s.resolve(requested)
	if err != nil {
		return nil, err
	}
	return s.openRegular(joined)
}

// openRegular 将 joined 作为工作区内已存在的普通文件打开。最终的
// 打开通过 openContained 进行，因此校验后被替换进来的 symlink 会
// 安全地失败，而不是重定向读取。
func (s *FileStore) openRegular(joined string) (*os.File, error) {
	resolved, err := s.resolveExisting(joined)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, mapResolveError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegularFile
	}
	return s.openContained(resolved)
}

// openContained 通过 os.Root 相对于 symlink 解析后的工作区根目录
// 重新打开已校验的绝对路径。Root.Open 会跟随 symlink，但绝不会解析
// 根目录之外的路径分量，因此把文件替换为链接或目录的竞态会表现为
// open/stat 错误，而不是逃逸。返回的文件描述符独立于 root 句柄，
// 因此 root 在打开完成后立即关闭。
func (s *FileStore) openContained(resolved string) (*os.File, error) {
	rel, err := filepath.Rel(s.inside, resolved)
	if err != nil || isDotDotPath(rel) {
		return nil, ErrOutsideWorkspace
	}
	root, err := os.OpenRoot(s.inside)
	if err != nil {
		return nil, mapResolveError(err)
	}
	file, err := root.Open(filepath.ToSlash(rel))
	if err != nil {
		return nil, errors.Join(mapResolveError(err), root.Close())
	}
	if closeErr := root.Close(); closeErr != nil {
		return nil, errors.Join(
			fmt.Errorf("close workspace root: %w", closeErr), file.Close())
	}
	info, statErr := file.Stat()
	if statErr == nil && info.Mode().IsRegular() {
		return file, nil
	}
	return nil, errors.Join(ErrNotRegularFile, statErr, file.Close())
}

// readExisting 以普通文件方式返回 requested 的完整内容。
func (s *FileStore) readExisting(requested string) ([]byte, error) {
	joined, err := s.resolve(requested)
	if err != nil {
		return nil, err
	}
	file, err := s.openRegular(joined)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(file)
	return data, errors.Join(readErr, file.Close())
}

// readLimited 最多读取 limit+1 个字节，当源文件更大时返回
// ErrTooLarge，避免部分内容被误当成完整内容。
func readLimited(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read workspace file: %w", err)
	}
	if len(data) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

// nodeVisitor 对遍历到的每个条目接收一个工作区内的相对路径。
type nodeVisitor func(rel string, entry fs.DirEntry) error

// walk 遍历根目录下的每个条目，且不跟随 symlink。
func (s *FileStore) walk(visitor nodeVisitor) error {
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == s.root {
			return nil
		}
		rel, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			return relErr
		}
		return visitor(rel, entry)
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("walk workspace files: %w", err)
	}
	return nil
}

// newFileNode 构建一个文件树节点；symlink 按普通文件呈现。
func newFileNode(rel string, entry fs.DirEntry) (FileNode, error) {
	info, err := entry.Info()
	if err != nil {
		return FileNode{}, fmt.Errorf("inspect workspace entry: %w", err)
	}
	nodeType := typeFile
	if entry.IsDir() {
		nodeType = typeDir
	}
	return FileNode{
		Path:    rel,
		Type:    nodeType,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}, nil
}

// resolve 校验 requested 并返回其位于工作区内的绝对路径：绝对路径
// 以及清理后仍包含 ".." 的路径都会被拒绝，并用 filepath.Rel 再次核验
// 拼接结果。这里刻意不使用 securejoin：它的 chroot 钳制总是把 symlink
// 目标重新锚定到根目录内，因此其返回值不携带任何逃逸信号，而且采用它
// 会抹掉链接路径本身——checkWriteTarget、regularTarget 和 resolveExisting
// 必须对这些路径执行 Lstat，才能在打开时检测出 symlink 目标。
func (s *FileStore) resolve(requested string) (string, error) {
	if filepath.IsAbs(requested) {
		return emptyPath, ErrOutsideWorkspace
	}
	cleaned := filepath.Clean(requested)
	if isDotDotPath(cleaned) {
		return emptyPath, ErrOutsideWorkspace
	}
	joined := filepath.Join(s.root, cleaned)
	rel, err := filepath.Rel(s.root, joined)
	if err != nil || isDotDotPath(rel) {
		return emptyPath, ErrOutsideWorkspace
	}
	return joined, nil
}

// resolveExisting 对已存在的路径重新解析 symlink，并要求解析结果
// 仍位于工作区根目录内。
func (s *FileStore) resolveExisting(joined string) (string, error) {
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return emptyPath, mapResolveError(err)
	}
	if err = s.ensureInside(resolved); err != nil {
		return emptyPath, err
	}
	return resolved, nil
}

// ensureInside 要求 abs 位于 symlink 解析后的根目录之内。
func (s *FileStore) ensureInside(abs string) error {
	rel, err := filepath.Rel(s.inside, abs)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOutsideWorkspace, err)
	}
	if isDotDotPath(rel) {
		return fmt.Errorf("%w: %s", ErrOutsideWorkspace, rel)
	}
	return nil
}

// isDotDotPath 判断清理后的路径是否通过父目录引用向外逃逸。
func isDotDotPath(cleaned string) bool {
	return cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func mapResolveError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return fmt.Errorf("resolve workspace path: %w", err)
}
