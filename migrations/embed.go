// Package migrations 内嵌各数据库方言的迁移脚本。
// 通过 go:embed 打包进二进制，保证安装后的单文件分发无需携带 SQL 文件。
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed sqlite postgres
var files embed.FS

// FS 返回内嵌迁移脚本的只读文件系统，路径形如 sqlite/000001_init.up.sql。
func FS() fs.FS { return files }
