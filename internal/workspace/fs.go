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
	// maxListNodes bounds the file tree returned for one workspace.
	maxListNodes = 1000
	// maxSearchResults bounds relative-path search matches.
	maxSearchResults = 200
	// emptyCapacity builds empty slices with a preset length bound.
	emptyCapacity = 0
	// typeDir and typeFile are the node types of the workspace file tree.
	typeDir  = "dir"
	typeFile = "file"
)

var (
	// ErrOutsideWorkspace marks paths escaping the workspace root.
	ErrOutsideWorkspace = errors.New("path escapes workspace root")
	// ErrNotFound marks workspace paths that do not exist.
	ErrNotFound = errors.New("workspace path not found")
	// ErrTooLarge marks files larger than the configured read limit.
	ErrTooLarge = errors.New("file exceeds the read limit")
	// ErrNotRegularFile marks directories and other non-file targets.
	ErrNotRegularFile = errors.New("workspace path is not a regular file")
)

// FileNode is one entry of the workspace file tree.
type FileNode struct {
	Path    string    `json:"path"`
	Type    string    `json:"type"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// FileChange is a reviewed, not-yet-applied file mutation. Apply re-validates
// the path at execution time so symlink swaps cannot escape the workspace.
type FileChange struct {
	Diff  string
	Apply func(context.Context) error
}

// FileStore reads workspace files safely and pre-computes reviewed changes.
// Write operations only materialize when FileChange.Apply runs.
type FileStore struct {
	root   string
	inside string
	limit  int
}

// NewFileStore builds a FileStore for one workspace directory. limit bounds
// Read payloads; the root's symlink resolution is cached for containment
// checks so a symlinked workspace root still accepts its own files.
func NewFileStore(root string, limit int) *FileStore {
	inside, err := filepath.EvalSymlinks(root)
	if err != nil {
		inside = root
	}
	return &FileStore{root: root, inside: inside, limit: limit}
}

// Read returns at most limit bytes of a regular file inside the workspace.
func (s *FileStore) Read(_ context.Context, requested string) ([]byte, error) {
	file, err := s.openReadFile(requested)
	if err != nil {
		return nil, err
	}
	data, readErr := readLimited(file, s.limit)
	return data, errors.Join(readErr, file.Close())
}

// List returns the workspace file tree capped at maxListNodes nodes. Symbolic
// links are listed but never followed into their targets.
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

// Search returns up to maxSearchResults relative paths containing query.
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

// openReadFile resolves, validates and opens requested for reading. Callers
// must close the returned file.
func (s *FileStore) openReadFile(requested string) (*os.File, error) {
	joined, err := s.resolve(requested)
	if err != nil {
		return nil, err
	}
	return s.openRegular(joined)
}

// openRegular opens joined as an existing regular file inside the workspace.
// The final open goes through openContained so a symlink swapped into place
// after validation fails closed instead of redirecting the read.
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

// openContained re-opens the validated absolute path relative to the
// symlink-resolved workspace root via os.Root. Root.Open follows symlinks but
// never resolves a component outside the root, so races that swap the file
// for a link or directory surface as open/stat errors instead of escapes.
// The returned file descriptor is independent of the root handle, which is
// therefore closed as soon as the open completes.
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

// readExisting returns the full contents of requested as a regular file.
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

// readLimited reads at most limit+1 bytes and fails with ErrTooLarge when the
// source is bigger, so partial files never masquerade as complete content.
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

// nodeVisitor receives one relative workspace path per walked entry.
type nodeVisitor func(rel string, entry fs.DirEntry) error

// walk visits every entry below the root without following symlinks.
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

// newFileNode builds one tree node; symlinks surface as plain files.
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

// resolve validates requested and returns its absolute path inside the
// workspace: absolute paths and paths still containing ".." after cleaning
// are rejected, and filepath.Rel re-verifies the joined result. securejoin is
// deliberately not used here: its chroot clamping always re-anchors symlink
// targets inside the root, so its return value carries no escape signal, and
// consuming it would erase the link paths that checkWriteTarget, regularTarget
// and resolveExisting must Lstat to detect symlinked targets at open time.
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

// resolveExisting re-resolves symlinks for an existing path and requires the
// result to stay inside the workspace root.
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

// ensureInside requires abs to stay inside the symlink-resolved root.
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

// isDotDotPath reports whether a cleaned path escapes through a parent link.
func isDotDotPath(cleaned string) bool {
	return cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}

func mapResolveError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return fmt.Errorf("resolve workspace path: %w", err)
}
