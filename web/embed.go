// Package web 嵌入构建好的前端产物，用于同源托管。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

var distRoot fs.FS

func init() {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	distRoot = sub
}

// Dist 返回以 dist 目录为根的嵌入式前端构建产物。
func Dist() fs.FS {
	return distRoot
}
