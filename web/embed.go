// Package web embeds the built single-page app.
//
// The directive lives here, not in internal/webui, because go:embed cannot
// reference a path outside its own directory and the build output is web/dist.
// The serving policy (cache headers, SPA fallback) is in internal/webui, which
// takes an fs.FS and so stays testable against a synthetic tree.
package web

import (
	"embed"
	"io/fs"
)

// dist is the Vite output directory. `all:` includes dotfiles, because Vite
// emits none today but an asset named with a leading dot would otherwise
// disappear from the binary without a word.
//
//go:embed all:dist
var dist embed.FS

// Dist is the app rooted at the build output, so its files are "index.html"
// and "assets/…" rather than "dist/index.html".
func Dist() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
