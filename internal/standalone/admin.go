package standalone

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentmesh/internal/catalog"
)

const adminCookieName = "agentmesh_admin"

type adminSession struct {
	CSRF    string
	Expires time.Time
}

type adminService struct {
	app          *App
	mu           sync.Mutex
	sessions     map[string]adminSession
	loginAttempt map[string][]time.Time
	localKeyOnce string
	ui           http.Handler
}

func newAdminService(app *App) *adminService {
	return &adminService{
		app: app, sessions: map[string]adminSession{}, loginAttempt: map[string][]time.Time{},
		localKeyOnce: app.localKey, ui: adminUIHandler(),
	}
}

func (a *App) AdminHandler() http.Handler {
	if a.admin == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "admin_not_available")
		})
	}
	return a.admin
}

func (s *adminService) takeLocalKeyOnce() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.localKeyOnce
	s.localKeyOnce = ""
	return value
}

func (s *adminService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin" {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/admin/") {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.URL.Path == "/admin/session":
		s.handleSession(w, r)
		return
	case r.URL.Path == "/admin/state":
		s.handleState(w, r)
		return
	case r.URL.Path == "/admin/providers" || strings.HasPrefix(r.URL.Path, "/admin/providers/"):
		s.handleProviders(w, r)
		return
	case r.URL.Path == "/admin/models" || strings.HasPrefix(r.URL.Path, "/admin/models/"):
		s.handleModels(w, r)
		return
	case r.URL.Path == "/admin/client-keys" || strings.HasPrefix(r.URL.Path, "/admin/client-keys/"):
		s.handleClientKeys(w, r)
		return
	case r.URL.Path == "/admin/routing-groups" || strings.HasPrefix(r.URL.Path, "/admin/routing-groups/"):
		s.handleRoutingGroups(w, r)
		return
	case r.URL.Path == "/admin/usage":
		s.handleUsage(w, r)
		return
	case r.URL.Path == "/admin/audit":
		s.handleAudit(w, r)
		return
	case r.URL.Path == "/admin/host/agents" || r.URL.Path == "/admin/host/scan" ||
		r.URL.Path == "/admin/host/local-models/scan" || r.URL.Path == "/admin/host/local-models/import":
		s.handleHost(w, r)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		// The login shell and its static assets must load before a session
		// exists. Every API route above remains authenticated.
		s.ui.ServeHTTP(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not_found")
}

func (s *adminService) handleSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		if !s.allowLogin(r) {
			writeError(w, http.StatusTooManyRequests, "admin_login_rate_limited")
			return
		}
		var input struct {
			Token string `json:"token"`
		}
		if err := decodeJSON(r, &input); err != nil || !s.app.adminTokenMatches(input.Token) {
			writeError(w, http.StatusUnauthorized, "admin_auth_failed")
			return
		}
		sessionID, err := randomToken("sess_")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "session_create_failed")
			return
		}
		csrf, err := randomToken("csrf_")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "session_create_failed")
			return
		}
		s.mu.Lock()
		s.sessions[sessionID] = adminSession{CSRF: csrf, Expires: time.Now().Add(12 * time.Hour)}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name: adminCookieName, Value: sessionID, Path: "/admin", HttpOnly: true,
			SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
		})
		writeJSON(w, http.StatusOK, map[string]any{"csrf": csrf, "expires_in": 12 * 60 * 60})
	case http.MethodDelete:
		s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
			if cookie, err := r.Cookie(adminCookieName); err == nil {
				s.mu.Lock()
				delete(s.sessions, cookie.Value)
				s.mu.Unlock()
			}
			http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: "", Path: "/admin", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w.WriteHeader(http.StatusNoContent)
		}, false)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *adminService) handleState(w http.ResponseWriter, r *http.Request) {
	s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		data, err := s.app.manager.ListData(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "catalog_read_failed")
			return
		}
		snapshot := s.app.manager.Snapshot()
		ready := snapshot != nil && s.app.manager.Repair() == "" && (len(snapshot.Models) > 0 || len(snapshot.Groups) > 0)
		writeJSON(w, http.StatusOK, map[string]any{
			"generation": data.Generation, "ready": ready, "repair": s.app.manager.Repair(),
			"providers": len(data.Providers), "models": len(data.Models), "enabled_models": len(snapshotModels(snapshot)),
			"client_keys": len(data.Keys), "local_key_once": s.takeLocalKeyOnce(),
		})
	}, true)
}

func (s *adminService) handleProviders(w http.ResponseWriter, r *http.Request) {
	s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
		parts := adminParts(r.URL.Path, "providers")
		switch {
		case len(parts) == 0 && r.Method == http.MethodGet:
			s.listProviders(w, r)
		case len(parts) == 0 && r.Method == http.MethodPost:
			s.createProvider(w, r)
		case len(parts) == 1 && r.Method == http.MethodGet:
			s.getProvider(w, r, parts[0])
		case len(parts) == 1 && r.Method == http.MethodPatch:
			s.updateProvider(w, r, parts[0])
		case len(parts) == 1 && r.Method == http.MethodDelete:
			if err := s.app.manager.DeleteProvider(r.Context(), parts[0]); err != nil {
				writeOperationError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost:
			s.testProvider(w, r, parts[0])
		case len(parts) == 2 && parts[1] == "discover" && r.Method == http.MethodPost:
			result, err := s.app.manager.DiscoverModels(r.Context(), parts[0])
			if err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		default:
			writeError(w, http.StatusNotFound, "not_found")
		}
	}, true)
}

func (s *adminService) handleModels(w http.ResponseWriter, r *http.Request) {
	s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
		parts := adminParts(r.URL.Path, "models")
		switch {
		case len(parts) == 0 && r.Method == http.MethodGet:
			data, err := s.app.manager.ListData(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "catalog_read_failed")
				return
			}
			out := make([]any, 0, len(data.Models))
			for _, model := range data.Models {
				out = append(out, safeModel(model))
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": out})
		case len(parts) == 0 && r.Method == http.MethodPost:
			var input struct {
				ProviderID string `json:"provider_id"`
				ModelInput
			}
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json")
				return
			}
			model, err := s.app.manager.CreateModel(r.Context(), input.ProviderID, input.ModelInput)
			if err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, safeModel(model))
		case len(parts) == 1 && r.Method == http.MethodPatch:
			var input ModelInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json")
				return
			}
			if err := s.app.manager.UpdateModel(r.Context(), parts[0], input); err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": parts[0], "updated": true})
		default:
			writeError(w, http.StatusNotFound, "not_found")
		}
	}, true)
}

func (s *adminService) handleClientKeys(w http.ResponseWriter, r *http.Request) {
	s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
		parts := adminParts(r.URL.Path, "client-keys")
		switch {
		case len(parts) == 0 && r.Method == http.MethodGet:
			data, err := s.app.manager.ListData(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "catalog_read_failed")
				return
			}
			out := make([]any, 0, len(data.Keys))
			for _, key := range data.Keys {
				out = append(out, safeClientKey(key))
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": out})
		case len(parts) == 0 && r.Method == http.MethodPost:
			var input ClientKeyInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json")
				return
			}
			key, plaintext, err := s.app.manager.CreateClientKey(r.Context(), input)
			if err != nil {
				writeOperationError(w, err)
				return
			}
			payload := safeClientKey(key)
			payload["plaintext_key"] = plaintext
			writeJSON(w, http.StatusCreated, payload)
		case len(parts) == 1 && r.Method == http.MethodPatch:
			var input ClientKeyInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json")
				return
			}
			if err := s.app.manager.UpdateClientKey(r.Context(), parts[0], input); err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": parts[0], "updated": true})
		case len(parts) == 1 && r.Method == http.MethodDelete:
			if err := s.app.manager.DeleteClientKey(r.Context(), parts[0]); err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": parts[0], "enabled": false})
		case len(parts) == 2 && parts[1] == "rotate" && r.Method == http.MethodPost:
			key, plaintext, err := s.app.manager.RotateClientKey(r.Context(), parts[0])
			if err != nil {
				writeOperationError(w, err)
				return
			}
			payload := safeClientKey(key)
			payload["plaintext_key"] = plaintext
			writeJSON(w, http.StatusCreated, payload)
		default:
			writeError(w, http.StatusNotFound, "not_found")
		}
	}, true)
}

func (s *adminService) handleRoutingGroups(w http.ResponseWriter, r *http.Request) {
	s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
		parts := adminParts(r.URL.Path, "routing-groups")
		switch {
		case len(parts) == 0 && r.Method == http.MethodGet:
			data, err := s.app.manager.ListData(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "catalog_read_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": data.RoutingGroups})
		case len(parts) == 0 && r.Method == http.MethodPost:
			var input RoutingGroupInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json")
				return
			}
			group, err := s.app.manager.UpsertRoutingGroup(r.Context(), input)
			if err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, group)
		case len(parts) == 1 && r.Method == http.MethodPatch:
			var input RoutingGroupInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json")
				return
			}
			input.Name = parts[0]
			group, err := s.app.manager.UpsertRoutingGroup(r.Context(), input)
			if err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, group)
		case len(parts) == 1 && r.Method == http.MethodDelete:
			if err := s.app.manager.DeleteRoutingGroup(r.Context(), parts[0]); err != nil {
				writeOperationError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusNotFound, "not_found")
		}
	}, true)
}

func (s *adminService) handleUsage(w http.ResponseWriter, r *http.Request) {
	s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		summary, err := s.app.manager.UsageSummary(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "usage_read_failed")
			return
		}
		writeJSON(w, http.StatusOK, summary)
	}, true)
}

func (s *adminService) handleAudit(w http.ResponseWriter, r *http.Request) {
	s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		events, err := s.app.manager.AuditEvents(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "audit_read_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": events})
	}, true)
}

func (s *adminService) handleHost(w http.ResponseWriter, r *http.Request) {
	s.requireSessionOrWrite(w, r, func(w http.ResponseWriter, r *http.Request, _ adminSession) {
		switch {
		case r.URL.Path == "/admin/host/agents" && r.Method == http.MethodGet:
			result, err := s.app.ScanHostAgents(r.Context(), HostScanInput{})
			if err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		case r.URL.Path == "/admin/host/scan" && r.Method == http.MethodPost:
			input := HostScanInput{}
			if r.ContentLength != 0 {
				if err := decodeJSON(r, &input); err != nil {
					writeError(w, http.StatusBadRequest, "invalid_json")
					return
				}
			}
			result, err := s.app.ScanHostAgents(r.Context(), input)
			if err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		case r.URL.Path == "/admin/host/local-models/scan" && r.Method == http.MethodPost:
			var input LocalModelScanInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json")
				return
			}
			sources, err := s.app.ScanLocalModels(r.Context(), input)
			if err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"sources": sources})
		case r.URL.Path == "/admin/host/local-models/import" && r.Method == http.MethodPost:
			var input LocalModelImportInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_json")
				return
			}
			result, err := s.app.manager.ImportLocalModels(r.Context(), input)
			if err != nil {
				writeOperationError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{
				"provider": safeProvider(result.Provider),
				"models":   result.Models,
				"result":   result.Result,
			})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		}
	}, true)
}

func (s *adminService) listProviders(w http.ResponseWriter, r *http.Request) {
	data, err := s.app.manager.ListData(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalog_read_failed")
		return
	}
	snapshot := s.app.manager.Snapshot()
	out := make([]any, 0, len(data.Providers))
	for _, provider := range data.Providers {
		view := safeProvider(provider)
		if credential, ok := data.Credentials[provider.ID]; ok {
			view["has_credential"] = true
			view["credential_mask"] = credential.Mask
		} else {
			view["has_credential"] = false
			view["credential_mask"] = ""
		}
		if snapshot != nil {
			for _, pv := range snapshot.Providers {
				if pv.Catalog.ID == provider.ID {
					view["runtime_error"] = pv.Error
					view["runtime_available"] = pv.Adapter != nil
					break
				}
			}
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *adminService) createProvider(w http.ResponseWriter, r *http.Request) {
	var input ProviderInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	provider, err := s.app.manager.CreateProvider(r.Context(), input)
	if err != nil {
		writeOperationError(w, err)
		return
	}
	view := safeProvider(provider)
	view["has_credential"] = input.AuthType != catalog.AuthNone
	writeJSON(w, http.StatusCreated, view)
}

func (s *adminService) getProvider(w http.ResponseWriter, r *http.Request, id string) {
	data, err := s.app.manager.ListData(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "catalog_read_failed")
		return
	}
	for _, provider := range data.Providers {
		if provider.ID == id {
			view := safeProvider(provider)
			if credential, ok := data.Credentials[id]; ok {
				view["has_credential"] = true
				view["credential_mask"] = credential.Mask
			} else {
				view["has_credential"] = false
			}
			writeJSON(w, http.StatusOK, view)
			return
		}
	}
	writeError(w, http.StatusNotFound, "provider_not_found")
}

func (s *adminService) updateProvider(w http.ResponseWriter, r *http.Request, id string) {
	var input ProviderInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	provider, err := s.app.manager.UpdateProvider(r.Context(), id, input)
	if err != nil {
		writeOperationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, safeProvider(provider))
}

func (s *adminService) testProvider(w http.ResponseWriter, r *http.Request, id string) {
	var input struct {
		Kind    string `json:"kind"`
		Model   string `json:"model"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	result, err := s.app.manager.TestProvider(r.Context(), id, input.Kind, input.Model, input.Confirm)
	if err != nil {
		writeOperationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *adminService) requireSessionOrWrite(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request, adminSession), mutation bool) {
	session, ok := s.session(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "admin_auth_required")
		return
	}
	if mutation && r.Method != http.MethodGet && r.Method != http.MethodHead {
		if r.Header.Get("X-CSRF-Token") != session.CSRF {
			writeError(w, http.StatusForbidden, "csrf_failed")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			writeError(w, http.StatusForbidden, "origin_failed")
			return
		}
	}
	next(w, r, session)
}

func (s *adminService) session(r *http.Request) (adminSession, bool) {
	cookie, err := r.Cookie(adminCookieName)
	if err != nil || cookie.Value == "" {
		return adminSession{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[cookie.Value]
	if !ok || time.Now().After(session.Expires) {
		delete(s.sessions, cookie.Value)
		return adminSession{}, false
	}
	return session, true
}

func (s *adminService) allowLogin(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := s.loginAttempt[host]
	filtered := attempts[:0]
	for _, at := range attempts {
		if at.After(cutoff) {
			filtered = append(filtered, at)
		}
	}
	if len(filtered) >= 5 {
		s.loginAttempt[host] = filtered
		return false
	}
	s.loginAttempt[host] = append(filtered, now)
	return true
}

func safeProvider(p catalog.Provider) map[string]any {
	return map[string]any{
		"id": p.ID, "slug": p.Slug, "name": p.Name, "base_url": p.BaseURL, "kind": p.Kind,
		"auth_type": p.AuthType, "tls_insecure": p.TLSInsecure, "enabled": p.Enabled, "revision": p.Revision,
		"created_at": p.CreatedAt, "updated_at": p.UpdatedAt,
	}
}

func safeModel(m catalog.Model) map[string]any {
	return map[string]any{
		"id": m.ID, "provider_id": m.ProviderID, "upstream_id": m.UpstreamID, "public_id": m.PublicID,
		"enabled": m.Enabled, "stale": m.Stale, "tools": m.Tools, "vision": m.Vision,
		"max_output_tokens": m.MaxOutputTokens, "source": m.Source,
		"created_at": m.CreatedAt, "updated_at": m.UpdatedAt,
	}
}

func safeClientKey(k catalog.ClientKey) map[string]any {
	return map[string]any{
		"id": k.ID, "name": k.Name, "prefix": k.Prefix, "enabled": k.Enabled, "scopes": k.Scopes,
		"rpm": k.RPM, "daily_requests": k.DailyRequests, "daily_tokens": k.DailyTokens,
		"model_allowlist": k.ModelAllowlist, "last_used_at": k.LastUsedAt, "expires_at": k.ExpiresAt,
		"created_at": k.CreatedAt,
	}
}

func snapshotModels(snapshot *Snapshot) []string {
	if snapshot == nil {
		return nil
	}
	return snapshot.ModelList
}

func adminParts(path, prefix string) []string {
	raw := strings.TrimPrefix(path, "/admin/"+prefix)
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "/")
	for index := range parts {
		if value, err := url.PathUnescape(parts[index]); err == nil {
			parts[index] = value
		}
	}
	return parts
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing_json")
	}
	return nil
}

func sameOrigin(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return parsed.Host == host && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func writeOperationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, catalog.ErrConflict):
		writeError(w, http.StatusConflict, "conflict")
	case strings.Contains(err.Error(), "local_models_import_requires_confirm") ||
		strings.Contains(err.Error(), "local_models_empty"):
		writeError(w, http.StatusUnprocessableEntity, stableCode(err))
	case strings.Contains(err.Error(), "requires_confirm"):
		writeError(w, http.StatusUnprocessableEntity, "chat_test_requires_confirm")
	case strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "immutable"):
		writeError(w, http.StatusUnprocessableEntity, stableCode(err))
	case strings.Contains(err.Error(), "unreachable") || strings.Contains(err.Error(), "unreadable") || strings.Contains(err.Error(), "status_"):
		writeError(w, http.StatusBadGateway, stableCode(err))
	case strings.Contains(err.Error(), "local_models_status") || strings.Contains(err.Error(), "local_models_unreachable"):
		writeError(w, http.StatusBadGateway, stableCode(err))
	case strings.Contains(err.Error(), "local_models_endpoint") || strings.Contains(err.Error(), "local_models_kind"):
		writeError(w, http.StatusUnprocessableEntity, stableCode(err))
	default:
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

func stableCode(err error) string {
	code := strings.TrimSpace(err.Error())
	if code == "" {
		return "operation_failed"
	}
	return code
}

var _ = context.Background
