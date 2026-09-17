// Package ui embeds the zarf-api web UI and serves its static assets.
package ui

import (
	"embed"
	"net/http"
)

//go:embed index.html app.js style.css
var assets embed.FS

// Handler serves the embedded UI assets. prefix is the mount path including
// a trailing slash (e.g. "/ui/" or "/zarf-api/ui/"); it is stripped so the
// index and its relatively-referenced assets resolve under any base path.
// Assets are embedded in the binary and change with every release, so they
// are served with no-cache to make browsers always revalidate — otherwise a
// cached app.js can silently outlive a server upgrade.
func Handler(prefix string) http.Handler {
	fs := http.FileServerFS(assets)
	return http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fs.ServeHTTP(w, r)
	}))
}
