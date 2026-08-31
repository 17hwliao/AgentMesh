package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"agentmesh/internal/tenant"
)

const (
	CodeAuthFailed       = "admin_auth_failed"
	CodeRequestInvalid   = "admin_request_invalid"
	CodeTenantExists     = "tenant_exists"
	CodeTenantNotFound   = "tenant_not_found"
	CodeAPIKeyNotFound   = "api_key_not_found"
	CodeLifecycleFailure = "admin_operation_failed"
)

const maxRequestBytes = 1 << 20

type Handler struct {
	lifecycle tenant.Lifecycle
	tokenHash [sha256.Size]byte
	routeOK   func([]string) bool
}

func NewHandler(lifecycle tenant.Lifecycle, tokenHash [sha256.Size]byte, routeOK func([]string) bool) *Handler {
	return &Handler{lifecycle: lifecycle, tokenHash: tokenHash, routeOK: routeOK}
}

// ServeHTTP verifies the independent admin credential before it calls any
// body reader. Tenant API keys can therefore never reach lifecycle handlers.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.lifecycle == nil || !h.authorized(r) {
		writeError(w, http.StatusUnauthorized, CodeAuthFailed)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/admin/tenants":
		h.createTenant(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/api-keys":
		h.createAPIKey(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/api-keys/"):
		h.revokeAPIKey(w, r, strings.TrimPrefix(r.URL.Path, "/admin/api-keys/"))
	default:
		writeError(w, http.StatusNotFound, CodeRequestInvalid)
	}
}

func (h *Handler) authorized(r *http.Request) bool {
	parts := strings.Fields(strings.TrimSpace(r.Header.Get("Authorization")))
	if len(parts) != 2 || parts[0] != "Bearer" {
		return false
	}
	digest := sha256.Sum256([]byte(parts[1]))
	return subtle.ConstantTimeCompare(digest[:], h.tokenHash[:]) == 1
}

func (h *Handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TenantID    string              `json:"tenant_id"`
		ModelRoutes map[string][]string `json:"model_routes"`
	}
	if !decodeStrict(w, r, &request) {
		return
	}
	if h.routeOK == nil {
		writeError(w, http.StatusBadRequest, CodeRequestInvalid)
		return
	}
	for _, route := range request.ModelRoutes {
		if !h.routeOK(route) {
			writeError(w, http.StatusBadRequest, CodeRequestInvalid)
			return
		}
	}
	err := h.lifecycle.CreateTenant(r.Context(), tenant.Tenant{ID: request.TenantID, Enabled: true, ModelRoutes: request.ModelRoutes})
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"tenant_id": request.TenantID})
}

func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TenantID string `json:"tenant_id"`
	}
	if !decodeStrict(w, r, &request) {
		return
	}
	key, raw, err := h.lifecycle.CreateAPIKey(r.Context(), request.TenantID)
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	// raw is deliberately scoped to this single success response.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]string{"key_id": key.ID, "api_key": raw})
}

func (h *Handler) revokeAPIKey(w http.ResponseWriter, r *http.Request, keyID string) {
	if keyID == "" || strings.Contains(keyID, "/") {
		writeError(w, http.StatusBadRequest, CodeRequestInvalid)
		return
	}
	if err := h.lifecycle.RevokeAPIKey(r.Context(), keyID); err != nil {
		writeLifecycleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeStrict(w http.ResponseWriter, r *http.Request, output any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, CodeRequestInvalid)
		return false
	}
	return true
}

func writeLifecycleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tenant.ErrTenantInvalid):
		writeError(w, http.StatusBadRequest, CodeRequestInvalid)
	case errors.Is(err, tenant.ErrTenantExists):
		writeError(w, http.StatusConflict, CodeTenantExists)
	case errors.Is(err, tenant.ErrTenantNotFound), errors.Is(err, tenant.ErrTenantDisabled):
		writeError(w, http.StatusNotFound, CodeTenantNotFound)
	case errors.Is(err, tenant.ErrKeyNotFound):
		writeError(w, http.StatusNotFound, CodeAPIKeyNotFound)
	default:
		writeError(w, http.StatusServiceUnavailable, CodeLifecycleFailure)
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
