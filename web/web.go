// Package web embeds the frontend so the server ships as a single binary.
package web

import (
	"embed"
	"io/fs"
)

// The go:embed directive copies everything under static/ into the compiled
// binary at build time; no files need to be deployed alongside it.
//
//go:embed static
var files embed.FS

// Static returns the frontend files rooted at static/, so "index.html" (not
// "static/index.html") is served at "/".
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		// fs.Sub only fails for an invalid path, which would be a
		// programming error caught by the first test run.
		panic(err)
	}
	return sub
}
