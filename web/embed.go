//go:build embedui

// Package web embeds the built single-page app (web/dist) into the binary.
// It only compiles with the embedui build tag, which requires `pnpm --dir web
// build` to have produced web/dist first; without the tag, internal/webui
// serves the UI from a directory instead.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built UI rooted at web/dist (index.html at its top).
func Dist() (fs.FS, error) { return fs.Sub(dist, "dist") }
