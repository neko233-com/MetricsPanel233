//go:build webui

// Package web embeds the production frontend into the server binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var bundle embed.FS

var Files = func() fs.FS {
	files, err := fs.Sub(bundle, "dist")
	if err != nil {
		panic(err)
	}
	return files
}()
