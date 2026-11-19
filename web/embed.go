// Package web embeds the built frontend distribution for same-origin hosting.
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

// Dist returns the embedded frontend build rooted at the dist directory.
func Dist() fs.FS {
	return distRoot
}
