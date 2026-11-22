package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

// CreateDirectory 在已存在的 parent 下创建单个子目录。
// parent 为空时回退到用户主目录；name 必须是单层目录名，禁止路径分隔符，
// 因此不会逃逸出 parent。
func CreateDirectory(parent, name string) (Directory, error) {
	if !validDirectoryName(name) {
		return Directory{}, fmt.Errorf("%w: invalid directory name", ErrInvalidPath)
	}
	resolved, err := resolveBrowsePath(parent)
	if err != nil {
		return Directory{}, err
	}
	target := filepath.Join(resolved, name)
	if err = os.Mkdir(target, dirPermission); err != nil {
		return Directory{}, wrapPathError(err)
	}
	return Directory{Name: name, Path: target}, nil
}

// validDirectoryName 只接受单层目录名，拒绝空值、相对路径片段与分隔符。
func validDirectoryName(name string) bool {
	return name != emptyPath && name != "." && name != ".." &&
		filepath.Base(name) == name
}
