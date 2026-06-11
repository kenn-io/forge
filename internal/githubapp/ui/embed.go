// Package ui embeds the built GitHub App setup frontend
// (packages/github-app-ui) served by middleman-github-app's loopback
// flow server. Like internal/web, the dist directory holds only a
// committed stub until `make build` copies the real Vite output in.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Assets returns the embedded setup frontend dist filesystem.
func Assets() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}

// HasBuiltApp reports whether the embedded dist contains a built
// frontend rather than only the committed stub. Binaries built with
// plain `go build` (no `make build`) lack the setup page; the CLI
// warns so the browser step is not a silent dead end.
func HasBuiltApp() bool {
	assets, err := Assets()
	if err != nil {
		return false
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return false
	}
	return true
}
