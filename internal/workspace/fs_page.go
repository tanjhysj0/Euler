package workspace

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

const (
	// defaultReadLines 与 maxReadLines 控制单页返回的行数。
	defaultReadLines = 2000
	maxReadLines     = 5000
	// defaultPageSize 与 maxPageSize 控制目录条目与搜索结果的分页大小。
	defaultPageSize = 200
	maxPageSize     = 500
	// maxLineBytes 限制单行长度，避免压缩产物等超长行耗尽内存。
	maxLineBytes = 1024 * 1024
	// initialLineBuffer 是扫描器的初始缓冲大小，避免小文件反复扩容。
	initialLineBuffer = 64 * 1024
	// firstLine 是行号起点，行号对模型是 1-based。
	firstLine = 1
	// noLimit 表示调用方没有指定分页大小。
	noLimit = 0
	// countOffset 把「行数」换算成「最后一个行号的偏移」。
	countOffset = 1
)

// ErrLineTooLong 表示页内出现超过单行上限的行。
// 此时无法按行切分，调用方应改用 shell 工具处理该文件。
var ErrLineTooLong = errors.New("line exceeds the read limit")

// LinePage 是一段按行切分的文件内容。
// NextOffset 是下一页的起始行号，仅在 HasMore 为 true 时有意义。
type LinePage struct {
	Content    string `json:"content"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	HasMore    bool   `json:"has_more"`
	NextOffset int    `json:"next_offset"`
}

// NodePage 是目录直接子项的一页。
// NextCursor 是下一页的游标，仅在 HasMore 为 true 时有意义。
type NodePage struct {
	Entries    []FileNode `json:"entries"`
	HasMore    bool       `json:"has_more"`
	NextCursor string     `json:"next_cursor"`
}

// SearchPage 是按路径搜索的结果页。
type SearchPage struct {
	Matches    []string `json:"matches"`
	HasMore    bool     `json:"has_more"`
	NextCursor string   `json:"next_cursor"`
}

// ReadLines 按行返回文件的一段内容，offset 从 1 开始。
// 只缓冲单页数据，因此读取超大文本不会撑爆内存：行数与单行长度
// 都有上限，超长行返回 ErrLineTooLong 而不是截断。
func (s *FileStore) ReadLines(
	_ context.Context, requested string, offset, limit int,
) (page LinePage, err error) {
	file, openErr := s.openReadFile(requested)
	if openErr != nil {
		return LinePage{}, openErr
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	offset, limit = normalizePage(offset, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, emptyCapacity, initialLineBuffer), maxLineBytes)
	page = LinePage{StartLine: offset}
	lines := make([]string, emptyCapacity, limit)
	line := emptyCapacity
	hasMore := false
	for scanner.Scan() {
		line++
		if line < offset {
			continue
		}
		// 收满一页后多读到的这一行只用于确认后续还有内容。
		if len(lines) >= limit {
			hasMore = true
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return LinePage{}, mapScanError(err)
	}
	if len(lines) > emptyCapacity {
		page.EndLine = offset + len(lines) - countOffset
	}
	page.Content = strings.Join(lines, "\n")
	page.HasMore = hasMore
	if hasMore {
		page.NextOffset = offset + len(lines)
	}
	return page, nil
}

// ListDir 返回目录直接子项的一页。cursor 是上一页最后一条的相对路径，
// 按文件名字典序继续，因此翻页期间目录变化只会影响边界附近的条目，
// 不会让已读条目重复出现。
func (s *FileStore) ListDir(
	_ context.Context, dir, cursor string, limit int,
) (NodePage, error) {
	resolved, err := s.resolveDirectory(dir)
	if err != nil {
		return NodePage{}, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return NodePage{}, mapResolveError(err)
	}
	limit = normalizeLimit(limit, defaultPageSize, maxPageSize)
	page := NodePage{Entries: make([]FileNode, emptyCapacity, limit)}
	for _, entry := range entries {
		node, nodeErr := s.dirNode(resolved, entry)
		if nodeErr != nil {
			return NodePage{}, nodeErr
		}
		if cursor != emptyPath && node.Path <= cursor {
			continue
		}
		if len(page.Entries) >= limit {
			page.HasMore = true
			return page, nil
		}
		page.Entries = append(page.Entries, node)
		page.NextCursor = node.Path
	}
	return page, nil
}

// Search 按路径关键词搜索工作区并返回一页结果。
// 只匹配工作区相对路径，不读取文件内容；内容检索请用 shell 工具。
func (s *FileStore) Search(
	_ context.Context, query, cursor string, limit int,
) (SearchPage, error) {
	limit = normalizeLimit(limit, defaultPageSize, maxPageSize)
	page := SearchPage{Matches: make([]string, emptyCapacity, limit)}
	err := s.walk(func(rel string, _ fs.DirEntry) error {
		if !strings.Contains(rel, query) || rel <= cursor {
			return nil
		}
		if len(page.Matches) >= limit {
			page.HasMore = true
			return fs.SkipAll
		}
		page.Matches = append(page.Matches, rel)
		page.NextCursor = rel
		return nil
	})
	if err != nil {
		return SearchPage{}, err
	}
	return page, nil
}

// normalizePage 把行分页参数收敛到合法范围，返回起始行号与每页行数。
func normalizePage(offset, limit int) (startLine int, pageLines int) {
	if offset < firstLine {
		offset = firstLine
	}
	if limit <= noLimit {
		limit = defaultReadLines
	}
	if limit > maxReadLines {
		limit = maxReadLines
	}
	return offset, limit
}

// normalizeLimit 把条目分页大小收敛到合法范围。
func normalizeLimit(limit, defaultLimit, maxLimit int) int {
	switch {
	case limit <= noLimit:
		return defaultLimit
	case limit > maxLimit:
		return maxLimit
	default:
		return limit
	}
}

// mapScanError 把扫描错误映射为工作区哨兵错误。
func mapScanError(err error) error {
	if errors.Is(err, bufio.ErrTooLong) {
		return ErrLineTooLong
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return fmt.Errorf("read workspace lines: %w", err)
}
