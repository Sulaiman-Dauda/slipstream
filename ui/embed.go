// Package ui embeds the built frontend so the panel ships as one binary.
// Run `make ui` (or `npm run build` in ui/) to refresh dist/.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
