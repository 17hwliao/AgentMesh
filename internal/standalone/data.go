package standalone

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/gateway"
	"agentmesh/internal/identity"
)

func (a *App) DataHandler() http.Handler {
	server := gateway.NewWithTenantRouting(a.resolver, a.manager)
	authenticated := server.AuthenticatedHandler(a.authenticate)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/readyz", a.handleReady)
	mux.Handle("/v1/chat/completions", authenticated)
	mux.Handle("/v1/models", authenticated)
	mux.Handle("/v1/models/", authenticated)
	a.registerExtendedDataRoutes(mux)
	if a.limiter == nil {
		a.limiter = newDataLimiter()
	}
	return a.dataGuard(mux)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	ping, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := a.store.DB.PingContext(ping); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "reason": "catalog_unavailable"})
		return
	}
	snapshot := a.manager.Snapshot()
	if snapshot == nil || a.manager.Repair() != "" || (len(snapshot.Models) == 0 && len(snapshot.Groups) == 0) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "repair": a.manager.Repair()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "generation": snapshot.Generation})
}

func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(r.Header.Get("Authorization"))
		parts := strings.Fields(raw)
		if len(parts) != 2 || parts[0] != "Bearer" || len(parts[1]) < 8 {
			writeError(w, http.StatusUnauthorized, "auth_failed")
			return
		}
		key, ok := a.manager.authenticate(parts[1][:8], hashBytes(parts[1]))
		if !ok {
			writeError(w, http.StatusUnauthorized, "auth_failed")
			return
		}
		kind := identity.KindAppKey
		if key.Name == "local" {
			kind = identity.KindLocal
		}
		principal := identity.Principal{
			ID: key.ID, TenantID: key.TenantID, Name: key.Name, Kind: kind, Scopes: key.Scopes,
		}
		ctx := identity.WithPrincipal(r.Context(), principal)
		// The existing gateway consumes a tenant ID. Standalone uses the stable
		// key ID so per-key policy and usage never depend on display names.
		ctx = auth.WithTenantID(ctx, key.ID)
		touch, cancel := context.WithTimeout(context.Background(), time.Second)
		a.manager.TouchClientKey(touch, key.ID)
		cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func hashBytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}
