//go:build !webui

package web

import (
	"embed"
	"io/fs"
)

// Keep API-only builds and go install usable without Node/build artifacts.
//
//go:embed fallback
var bundle embed.FS

var Files = func() fs.FS {
	files, err := fs.Sub(bundle, "fallback")
	if err != nil {
		panic(err)
	}
	return files
}()
