package server

import (
	"embed"
	"io/fs"
	"net/http"
)

// webDir is the embedded browser UI: the build output of the Vue source in the
// repository's top-level web/ directory, not hand-written files.
//
// The directory is the build output of web/ (Vue + Vite), and the output is
// committed: running the tool needs only `go build`, while changing the
// interface is what needs Node. Embedding `web` as a whole keeps this file
// free of a file list that the bundler would otherwise have to keep in sync.
//
//go:embed web
var webDir embed.FS

// staticHandler serves the embedded assets: "/" resolves to index.html, the
// hashed bundle lives under /assets/, and any other path is a 404. A broken
// embed surfaces as a 404 from http.FileServer rather than a panic.
func staticHandler() http.Handler {
	sub, err := fs.Sub(webDir, "web")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}
