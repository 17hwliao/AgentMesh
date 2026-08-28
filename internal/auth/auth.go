package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"agentmesh/internal/tenant"
)

const (
	CodeConfigurationMissing = "auth_configuration_missing"
	CodeConfigurationInvalid = "auth_configuration_invalid"
	CodeFailed               = "auth_failed"
)

type ConfigurationError struct{ Code string }

func (e *ConfigurationError) Error() string { return e.Code }

type contextKey struct{}

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func Bootstrap(lookup func(string) string) (*tenant.MemoryStore, error) {
	key, id, routesRaw := lookup("AGENTMESH_BOOTSTRAP_API_KEY"), lookup("AGENTMESH_BOOTSTRAP_TENANT_ID"), lookup("AGENTMESH_BOOTSTRAP_MODEL_ROUTES")
	if key == "" || id == "" || routesRaw == "" {
		return nil, &ConfigurationError{CodeConfigurationMissing}
	}
	if !tenantIDPattern.MatchString(id) || len(key) < 12 {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	var routes map[string][]string
	dec := json.NewDecoder(strings.NewReader(routesRaw))
	dec.DisallowUnknownFields()
	if dec.Decode(&routes) != nil || dec.Decode(&struct{}{}) != io.EOF || len(routes) == 0 {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	for model, route := range routes {
		if strings.TrimSpace(model) == "" || len(route) == 0 {
			return nil, &ConfigurationError{CodeConfigurationInvalid}
		}
		seen := map[string]bool{}
		for _, name := range route {
			if seen[name] || (name != "mock" && name != "ark" && name != "ollama") {
				return nil, &ConfigurationError{CodeConfigurationInvalid}
			}
			seen[name] = true
		}
	}
	digest := sha256.Sum256([]byte(key))
	prefix := key[:8]
	return tenant.NewMemory([]tenant.Tenant{{ID: id, Enabled: true, ModelRoutes: routes}}, []tenant.APIKeyRecord{{Prefix: prefix, Hash: digest, TenantID: id, Enabled: true}}), nil
}
func Authenticate(store tenant.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(r.Header.Get("Authorization"))
		parts := strings.Fields(raw)
		if len(parts) != 2 || parts[0] != "Bearer" {
			reject(w)
			return
		}
		key := parts[1]
		if len(key) < 8 {
			reject(w)
			return
		}
		t, ok := store.Authenticate(key[:8], sha256.Sum256([]byte(key)))
		if !ok {
			reject(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, t.ID)))
	})
}
func TenantID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(contextKey{}).(string)
	return id, ok
}
func reject(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": CodeFailed}})
}
func IsConfigurationError(err error) (string, bool) {
	var c *ConfigurationError
	if errors.As(err, &c) {
		return c.Code, true
	}
	return "", false
}
