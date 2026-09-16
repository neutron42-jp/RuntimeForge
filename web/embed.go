// Package web embeds the built front-end assets. Until the React build
// is produced, dist contains a placeholder index.html so the binary
// always serves something.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the distribution filesystem rooted at dist/.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
