package admin

import (
	"agentmesh/internal/auth"
	"agentmesh/internal/tenant"
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func page(r *http.Request) (string, int, bool) {
	after := r.URL.Query().Get("after")
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			return "", 0, false
		}
		limit = n
	}
	return after, limit, true
}
func (h *Handler) manage(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/admin/tenants" && !strings.HasPrefix(r.URL.Path, "/admin/tenants/") {
		return false
	}
	m, ok := h.lifecycle.(tenant.Management)
	if !ok {
		writeError(w, 503, CodeLifecycleFailure)
		return true
	}
	after, limit, ok := page(r)
	if !ok {
		writeError(w, 400, CodeRequestInvalid)
		return true
	}
	if r.URL.Path == "/admin/tenants" {
		if r.Method != "GET" {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		rows, e := m.ListAccounts(r.Context(), after, limit)
		if e != nil {
			writeLifecycleError(w, e)
			return true
		}
		next := ""
		if len(rows) == limit {
			next = rows[len(rows)-1].ID
		}
		writeJSON(w, 200, map[string]any{"data": rows, "next_after": next})
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/admin/tenants/"), "/")
	id := parts[0]
	if !tenant.ValidID(id) {
		writeError(w, 400, CodeRequestInvalid)
		return true
	}
	if len(parts) == 2 && parts[1] == "api-keys" && r.Method == "GET" {
		if _, e := m.Account(r.Context(), id); e != nil {
			writeLifecycleError(w, e)
			return true
		}
		rows, e := m.ListKeys(r.Context(), id, after, limit)
		if e != nil {
			writeLifecycleError(w, e)
			return true
		}
		next := ""
		if len(rows) == limit {
			next = rows[len(rows)-1].ID
		}
		writeJSON(w, 200, map[string]any{"data": rows, "next_after": next})
		return true
	}
	if len(parts) != 1 {
		writeError(w, 404, CodeRequestInvalid)
		return true
	}
	switch r.Method {
	case "GET":
		a, e := m.Account(r.Context(), id)
		if e != nil {
			writeLifecycleError(w, e)
		} else {
			writeJSON(w, 200, a)
		}
	case "PATCH":
		var patch tenant.AccountPatch
		if !decodeStrict(w, r, &patch) {
			return true
		}
		for _, route := range patch.ModelRoutes {
			if h.routeOK == nil || !h.routeOK(route) {
				writeError(w, 400, CodeRequestInvalid)
				return true
			}
		}
		if e := m.UpdateAccount(r.Context(), id, patch); e != nil {
			writeLifecycleError(w, e)
		} else {
			w.WriteHeader(204)
		}
	default:
		writeError(w, 405, "method_not_allowed")
	}
	return true
}

// AccountHandler returns only the principal proven by the current application
// key; no request parameter can select a different user's account.
func AccountHandler(m tenant.Management) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" {
			writeError(w, 405, "method_not_allowed")
			return
		}
		id, ok := auth.TenantID(r.Context())
		if !ok {
			writeError(w, 401, "auth_failed")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		a, e := m.Account(ctx, id)
		if e != nil {
			writeLifecycleError(w, e)
			return
		}
		// Provider routing is an administrator detail, not needed by client profiles.
		writeJSON(w, 200, map[string]any{"tenant_id": a.ID, "enabled": a.Enabled, "created_at": a.CreatedAt})
	})
}
