//go:build windows

package workspace

import (
	"os"

	"golang.org/x/sys/windows"
)

// driveLetters 是 Windows 逻辑盘符的探测范围（A-Z）。
const driveLetters = 26

// browseRoots 枚举当前存在的逻辑盘符，作为可切换的浏览入口。
func browseRoots() []Directory {
	mask, err := windows.GetLogicalDrives()
	if err != nil || mask == 0 {
		return nil
	}
	roots := make([]Directory, 0, driveLetters)
	for index := 0; index < driveLetters; index++ {
		if mask&(uint32(1)<<index) == 0 {
			continue
		}
		letter := string(rune('A' + index))
		roots = append(roots, Directory{
			Name: letter + ":",
			Path: letter + ":" + string(os.PathSeparator),
		})
	}
	return roots
}
