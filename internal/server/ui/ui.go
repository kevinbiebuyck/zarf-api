// Package ui embeds the zarf-api web UI and serves its static assets.
package ui

import (
	"embed"
	"net/http"
)

//go:embed index.html app.js style.css
var assets embed.FS

// Handler serves the embedded UI assets. Mount it at /ui/ — the prefix is
// stripped so /ui/ serves index.html and /ui/app.js etc. resolve relatively.
func Handler() http.Handler {
	return http.StripPrefix("/ui/", http.FileServerFS(assets))
}
