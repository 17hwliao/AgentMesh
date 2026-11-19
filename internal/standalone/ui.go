package standalone

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed ui/*
var adminUIAssets embed.FS

func adminUIHandler() http.Handler {
	root, err := fs.Sub(adminUIAssets, "ui")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusInternalServerError, "admin_ui_unavailable")
		})
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		clone := r.Clone(r.Context())
		clone.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, "/admin/")
		if clone.URL.Path == "//" {
			clone.URL.Path = "/"
		}
		files.ServeHTTP(w, clone)
	})
}
