//go:build !windows

package workspace

// browseRoots 在类 Unix 系统只提供文件系统根目录。
func browseRoots() []Directory {
	return []Directory{{Name: "/", Path: "/"}}
}
