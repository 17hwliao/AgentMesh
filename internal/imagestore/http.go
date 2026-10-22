package imagestore

import (
	"agentmesh/internal/auth"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

func (s *Store) Handler() http.Handler {
	// The route is authenticated by the application before this bounded work.
	slots := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		owner, ok := auth.TenantID(r.Context())
		if !ok {
			fail(w, 401, "auth_failed")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			fail(w, 429, "image_busy")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if r.URL.Path == "/v1/images" {
			if r.Method == "GET" {
				rows, e := s.List(ctx, owner, r.URL.Query().Get("after"))
				if e != nil {
					storeError(w, e)
					return
				}
				next := ""
				if len(rows) == 50 {
					next = rows[len(rows)-1].ID
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "next_after": next})
				return
			}
			if r.Method != "POST" {
				fail(w, 405, "method_not_allowed")
				return
			}
			media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if e != nil {
				fail(w, 400, "invalid_image")
				return
			}
			raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBytes))
			if e != nil {
				var bound *http.MaxBytesError
				if errors.As(e, &bound) {
					fail(w, 413, "image_too_large")
				} else {
					fail(w, 400, "invalid_image")
				}
				return
			}
			row, e := s.Put(ctx, owner, media, raw)
			if e != nil {
				storeError(w, e)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Location", "/v1/images/"+row.ID)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(row)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/images/")
		switch r.Method {
		case "GET":
			row, e := s.Get(ctx, owner, id)
			if e != nil {
				storeError(w, e)
				return
			}
			w.Header().Set("Content-Type", row.MediaType)
			w.Header().Set("Content-Disposition", `attachment; filename="`+row.ID+`.image"`)
			_, _ = w.Write(row.Content)
		case "DELETE":
			if e := s.Delete(ctx, owner, id); e != nil {
				storeError(w, e)
			} else {
				w.WriteHeader(204)
			}
		default:
			fail(w, 405, "method_not_allowed")
		}
	})
}
func storeError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrInvalid):
		fail(w, 400, "invalid_image")
	case errors.Is(e, ErrNotFound):
		fail(w, 404, "image_not_found")
	case errors.Is(e, ErrQuota):
		fail(w, 429, "image_quota_exceeded")
	default:
		fail(w, 503, "image_storage_unavailable")
	}
}
func fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}
