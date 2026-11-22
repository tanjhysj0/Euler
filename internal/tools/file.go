package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/components/tool"

	"euler/internal/workspace"
)

type fileReadInput struct {
	Path   string `json:"path" jsonschema:"description=工作区内的文件路径"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=起始行号（从 1 开始），省略表示从第 1 行读起"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=本次最多返回的行数，省略时默认 2000，上限 5000"`
}

const emptyPath = ""

const (
	// indexOffset 把长度换算成最后一个元素的下标。
	indexOffset = 1
	// lineFeedBytes 是拼回多行文本时每行额外的换行符字节数。
	lineFeedBytes = 1
)

type fileListInput struct {
	Path   string `json:"path" jsonschema:"description=要列出的目录，相对工作区根；省略则列出根目录"`
	Cursor string `json:"cursor,omitempty" jsonschema:"description=上一页返回的 next_cursor，省略表示第一页"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=每页条目数，省略时默认 200，上限 500"`
}

type fileSearchInput struct {
	Query  string `json:"query" jsonschema:"description=按路径匹配的关键词，不搜索文件内容"`
	Cursor string `json:"cursor,omitempty" jsonschema:"description=上一页返回的 next_cursor，省略表示第一页"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=每页结果数，省略时默认 200，上限 500"`
}

type fileWriteInput struct {
	Path    string `json:"path" jsonschema:"description=工作区内的文件路径"`
	Content string `json:"content" jsonschema:"description=完整文件内容"`
}

type filePatchInput struct {
	Path      string `json:"path" jsonschema:"description=工作区内的文件路径"`
	OldString string `json:"old_string" jsonschema:"description=待替换的唯一原文"`
	NewString string `json:"new_string" jsonschema:"description=替换后的文本"`
}

type fileDeleteInput struct {
	Path string `json:"path" jsonschema:"description=工作区内的文件路径"`
}

// fileToolset 将六个文件工具绑定到同一个工作区 FileStore。
type fileToolset struct {
	registry *Registry
	files    FileStore
}

func (r *Registry) fileTools(files FileStore) ([]tool.BaseTool, error) {
	set := fileToolset{registry: r, files: files}
	builders := []func() (tool.BaseTool, error){
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_read",
				"按行读取工作区内文件，支持 offset/limit 分页", set.read)
		},
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_list",
				"分页列出工作区内某个目录的直接子项", set.list)
		},
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_search",
				"按路径关键词分页搜索工作区，不搜文件内容", set.search)
		},
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_write", "写入完整文件内容（需用户批准）", set.write)
		},
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_patch", "替换文件中的唯一文本片段（需用户批准）", set.patch)
		},
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_delete", "删除工作区内的文件（需用户批准）", set.delete)
		},
	}
	built := make([]tool.BaseTool, emptyCapacity, len(builders))
	for _, build := range builders {
		builtTool, err := build()
		if err != nil {
			return nil, err
		}
		built = append(built, builtTool)
	}
	return built, nil
}

func (s fileToolset) read(ctx context.Context, input fileReadInput) (toolOutcome, error) {
	page, err := s.files.ReadLines(ctx, input.Path, input.Offset, input.Limit)
	if err != nil {
		return failedOutcome(fileFailure(err, input.Path)), nil
	}
	view := s.fitLines(page)
	payload := map[string]any{
		"path": input.Path, "content": view.content,
		"start_line": page.StartLine, "end_line": view.endLine, "has_more": view.hasMore,
	}
	if view.hasMore {
		payload["next_offset"] = view.nextOffset
	}
	return completedOutcome(payload), nil
}

// lineView 是在输出字节预算内裁剪后的行视图。
type lineView struct {
	content    string
	endLine    int
	hasMore    bool
	nextOffset int
}

// fitLines 在输出字节预算内按行回退内容，保证 start_line/end_line
// 与实际返回的文本一致，并给出下一页的起始行号。
func (s fileToolset) fitLines(page workspace.LinePage) lineView {
	view := lineView{
		content: page.Content, endLine: page.EndLine,
		hasMore: page.HasMore, nextOffset: page.NextOffset,
	}
	limit := s.registry.deps.MaxOutputBytes
	if limit <= noOutputLimit || len(view.content) <= limit {
		return view
	}
	lines := strings.Split(view.content, "\n")
	kept, size := emptyCapacity, emptyCapacity
	for _, line := range lines {
		if kept > emptyCapacity && size+len(line)+lineFeedBytes > limit {
			break
		}
		size += len(line) + lineFeedBytes
		kept++
	}
	if kept >= len(lines) {
		return view
	}
	return lineView{
		content:    strings.Join(lines[:kept], "\n"),
		endLine:    page.StartLine + kept - indexOffset,
		hasMore:    true,
		nextOffset: page.StartLine + kept,
	}
}

func (s fileToolset) list(ctx context.Context, input fileListInput) (toolOutcome, error) {
	page, err := s.files.ListDir(ctx, input.Path, input.Cursor, input.Limit)
	if err != nil {
		return failedOutcome(fileFailure(err, input.Path)), nil
	}
	entries := make([]map[string]any, emptyCapacity, len(page.Entries))
	for _, node := range page.Entries {
		entries = append(entries, map[string]any{
			"path": node.Path, "type": node.Type, "size": node.Size,
		})
	}
	payload := cappedList(s.registry, "entries", entries)
	kept, typed := payload["entries"].([]map[string]any)
	if !typed {
		kept = entries
	}
	nextCursor := page.NextCursor
	if len(kept) < len(entries) && len(kept) > emptyCapacity {
		if last, ok := kept[len(kept)-indexOffset]["path"].(string); ok {
			nextCursor = last
		}
	}
	delete(payload, "truncated")
	payload["has_more"] = page.HasMore || len(kept) < len(entries)
	payload["next_cursor"] = nextCursor
	return completedOutcome(payload), nil
}

func (s fileToolset) search(ctx context.Context, input fileSearchInput) (toolOutcome, error) {
	page, err := s.files.Search(ctx, input.Query, input.Cursor, input.Limit)
	if err != nil {
		return failedOutcome(fileFailure(err, emptyPath)), nil
	}
	matches := page.Matches
	if matches == nil {
		matches = []string{}
	}
	payload := cappedList(s.registry, "matches", matches)
	kept, typed := payload["matches"].([]string)
	if !typed {
		kept = matches
	}
	nextCursor := page.NextCursor
	if len(kept) < len(matches) && len(kept) > emptyCapacity {
		nextCursor = kept[len(kept)-indexOffset]
	}
	delete(payload, "truncated")
	payload["has_more"] = page.HasMore || len(kept) < len(matches)
	payload["next_cursor"] = nextCursor
	return completedOutcome(payload), nil
}

func (s fileToolset) write(ctx context.Context, input fileWriteInput) (toolOutcome, error) {
	change, err := s.files.PrepareWrite(ctx, input.Path, []byte(input.Content))
	if err != nil {
		return failedOutcome(fileFailure(err, input.Path)), nil
	}
	return s.mutationOutcome(input.Path, change), nil
}

func (s fileToolset) patch(ctx context.Context, input filePatchInput) (toolOutcome, error) {
	change, err := s.files.PreparePatch(ctx, input.Path, input.OldString, input.NewString)
	if err != nil {
		return failedOutcome(fileFailure(err, input.Path)), nil
	}
	return s.mutationOutcome(input.Path, change), nil
}

func (s fileToolset) delete(ctx context.Context, input fileDeleteInput) (toolOutcome, error) {
	change, err := s.files.Delete(ctx, input.Path)
	if err != nil {
		return failedOutcome(fileFailure(err, input.Path)), nil
	}
	return s.mutationOutcome(input.Path, change), nil
}

// mutationOutcome 将副作用推迟到审批通过之后：payload 携带
// 预先准备好的 diff，apply 原样重放这份已准备好的变更。
func (s fileToolset) mutationOutcome(path string, change workspace.FileChange) toolOutcome {
	return toolOutcome{
		payload: map[string]any{"path": path, "diff": s.registry.truncate(change.Diff)},
		apply: func(ctx context.Context) (toolOutcome, error) {
			if err := change.Apply(ctx); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return toolOutcome{}, ctxErr
				}
				return failedOutcome(fileFailure(err, path)), nil
			}
			return completedOutcome(map[string]any{"path": path}), nil
		},
	}
}

// fileFailure 将 workspace 的哨兵错误映射为稳定、面向用户的错误码。
func fileFailure(err error, path string) map[string]any {
	var code, message string
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		code, message = "file_not_found", "文件不存在："+path
	case errors.Is(err, workspace.ErrOutsideWorkspace):
		code, message = "path_outside_workspace", "路径超出工作区"
	case errors.Is(err, workspace.ErrTooLarge):
		code, message = "file_too_large", "文件过大"
	case errors.Is(err, workspace.ErrLineTooLong):
		code, message = "line_too_long", "存在超长行，无法按行读取；请改用 shell 工具处理"
	case errors.Is(err, workspace.ErrNotRegularFile):
		code, message = "not_regular_file", "不是普通文件"
	case errors.Is(err, workspace.ErrNotDirectory):
		code, message = "not_directory", "不是目录："+path
	case errors.Is(err, workspace.ErrPatchAmbiguous):
		code, message = "patch_ambiguous", "待替换文本不唯一或不存在"
	case errors.Is(err, workspace.ErrNotDeletable):
		code, message = "not_deletable", "只能删除工作区内的普通文件"
	default:
		code, message = "file_error", "工具执行失败"
	}
	return map[string]any{"error": map[string]any{"code": code, "message": message}}
}
