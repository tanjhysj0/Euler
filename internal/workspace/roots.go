package workspace

import (
	"path/filepath"
	"strings"
)

// breadcrumbsFrom 把绝对路径拆成从卷根到当前目录的层级，供前端直接渲染。
// Windows 的卷根是盘符（如 C:\），类 Unix 的卷根是 /。
func breadcrumbsFrom(current string) []Directory {
	root := volumeRoot(current)
	crumbs := []Directory{{Name: rootName(root), Path: root}}
	rel, err := filepath.Rel(root, current)
	if err != nil || rel == "." {
		return crumbs
	}
	path := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		crumbs = append(crumbs, Directory{Name: part, Path: path})
	}
	return crumbs
}

// volumeRoot 返回路径所在卷的根目录；无卷名时回退到文件系统根。
func volumeRoot(path string) string {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return string(filepath.Separator)
	}
	if strings.HasSuffix(volume, string(filepath.Separator)) {
		return volume
	}
	return volume + string(filepath.Separator)
}

// rootName 是根入口在面包屑中的显示名：Windows 显示盘符，类 Unix 显示 /。
func rootName(root string) string {
	trimmed := strings.TrimRight(root, string(filepath.Separator))
	if trimmed == "" {
		return string(filepath.Separator)
	}
	return trimmed
}
