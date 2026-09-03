// Package web embeds the SvelteKit app with SPA fallback.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var files embed.FS

const indexFile = "index.html"

// Returns the built app, nil when built without it
func FS() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, indexFile); err != nil {
		return nil
	}
	return sub
}

// Serves the app, falling back to index.html for routes
func Handler() http.Handler {
	app := FS()
	if app == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("nebu: the web UI was not built into this binary, run make web before make build\n"))
		})
	}
	static := http.FileServer(http.FS(app))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = indexFile
		}
		if _, err := fs.Stat(app, name); err != nil {
			if _, err := fs.Stat(app, name+".html"); err == nil {
				r.URL.Path = "/" + name + ".html"
			} else {
				r.URL.Path = "/"
			}
		}
		if strings.HasPrefix(name, "_app/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		static.ServeHTTP(w, r)
	})
}
