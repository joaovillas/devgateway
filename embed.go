// Package devgateway exposes the assets embedded in the binary.
package devgateway

import (
	"embed"
	"io/fs"
)

// The web/dist directory always exists: a minimal index.html is committed so
// that go build works on a clean clone, without having run the frontend build.
//
//go:embed all:web/dist
var webDist embed.FS

// WebFS returns the built frontend, rooted at web/dist.
func WebFS() fs.FS {
	sub, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		panic(err)
	}
	return sub
}
