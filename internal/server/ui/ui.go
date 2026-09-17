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
func Handler(prefix string) http.Handler {
	return http.StripPrefix(prefix, http.FileServerFS(assets))
}
