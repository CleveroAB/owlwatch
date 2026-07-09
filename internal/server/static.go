package server

import (
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/CleveroAB/owlwatch/web"
)

// newUIHandler serves the embedded frontend build with an SPA fallback:
// requests for real files are served as-is, everything else gets index.html
// so client-side routes deep-link correctly. It is registered on the mux's
// catch-all "/" pattern, so it must never shadow the API: /api/* paths that
// reach it are unknown endpoints and answer 404 JSON, not index.html.
func newUIHandler() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		// Unreachable with a valid embed; a panic here means the binary was
		// built without the frontend and cannot serve anything useful.
		panic(fmt.Sprintf("embedded UI missing: %v", err))
	}
	files := http.FileServerFS(dist)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "." {
			if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
				// Vite content-hashes everything under /assets/, so those
				// are immutable; other files (index.html, favicons) must
				// revalidate so deploys show up.
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}

		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, dist, "index.html")
	})
}
