package tools

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/components/tool"

	"euler/internal/workspace"
)

type fileReadInput struct {
	Path string `json:"path" jsonschema:"description=工作区内的文件路径"`
}

const emptyPath = ""

type fileListInput struct{}

type fileSearchInput struct {
	Query string `json:"query" jsonschema:"description=搜索关键词"`
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
	files    *workspace.FileStore
}

func (r *Registry) fileTools(files *workspace.FileStore) ([]tool.BaseTool, error) {
	set := fileToolset{registry: r, files: files}
	builders := []func() (tool.BaseTool, error){
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_read", "读取工作区内文件内容", set.read)
		},
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_list", "列出工作区内的文件与目录", set.list)
		},
		func() (tool.BaseTool, error) {
			return registerTool(r, "file_search", "按文件名或内容搜索工作区", set.search)
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
	data, err := s.files.Read(ctx, input.Path)
	if err != nil {
		return failedOutcome(fileFailure(err, input.Path)), nil
	}
	return completedOutcome(map[string]any{
		"path": input.Path, "content": s.registry.truncate(string(data)),
	}), nil
}

func (s fileToolset) list(ctx context.Context, _ fileListInput) (toolOutcome, error) {
	nodes, err := s.files.List(ctx)
	if err != nil {
		return failedOutcome(fileFailure(err, emptyPath)), nil
	}
	entries := make([]map[string]any, emptyCapacity, len(nodes))
	for _, node := range nodes {
		entries = append(entries, map[string]any{
			"path": node.Path, "type": node.Type, "size": node.Size,
		})
	}
	return completedOutcome(cappedList(s.registry, "entries", entries)), nil
}

func (s fileToolset) search(ctx context.Context, input fileSearchInput) (toolOutcome, error) {
	matches, err := s.files.Search(ctx, input.Query)
	if err != nil {
		return failedOutcome(fileFailure(err, emptyPath)), nil
	}
	if matches == nil {
		matches = []string{}
	}
	return completedOutcome(cappedList(s.registry, "matches", matches)), nil
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
	case errors.Is(err, workspace.ErrNotRegularFile):
		code, message = "not_regular_file", "不是普通文件"
	case errors.Is(err, workspace.ErrPatchAmbiguous):
		code, message = "patch_ambiguous", "待替换文本不唯一或不存在"
	case errors.Is(err, workspace.ErrNotDeletable):
		code, message = "not_deletable", "只能删除工作区内的普通文件"
	default:
		code, message = "file_error", "工具执行失败"
	}
	return map[string]any{"error": map[string]any{"code": code, "message": message}}
}
