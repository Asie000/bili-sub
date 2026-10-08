package server

import (
	"embed"
	"io/fs"
	"net/http"
)

// webDir is the embedded browser UI (Ruling B: it lives under
// internal/server/web, there is no top-level web/ directory). Listing the three
// assets explicitly makes a missing file a build error.
//
//go:embed web/index.html web/app.js web/style.css
var webDir embed.FS

// staticHandler serves the embedded assets: "/" resolves to index.html and the
// two side files are reachable at /app.js and /style.css. Any other path is a
// 404; a broken embed surfaces as a 404 from http.FileServer rather than a
// panic.
func staticHandler() http.Handler {
	sub, err := fs.Sub(webDir, "web")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}
