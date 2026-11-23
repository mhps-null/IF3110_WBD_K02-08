package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// NewSPAHandler serves a built Single-Page Application from distDir while
// ensuring unmatched /api/ requests always return JSON 404 instead of HTML.
func NewSPAHandler(distDir string) http.Handler {
	cleanDist := filepath.Clean(distDir)
	indexPath := filepath.Join(cleanDist, "index.html")
	distFS := os.DirFS(cleanDist)
	fileServer := http.FileServer(http.FS(distFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "endpoint tidak ditemukan"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}

		// Safely resolve requested static asset strictly inside distFS.
		rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if rel != "" && fs.ValidPath(rel) {
			if info, err := fs.Stat(distFS, rel); err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		if _, err := os.Stat(indexPath); err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("Frontend SPA belum dibangun. Jalankan 'cd frontend && npm install && npm run build' atau gunakan Vite dev server.\n"))
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, indexPath)
	})
}
