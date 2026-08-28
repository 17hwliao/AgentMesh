package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmesh/internal/tenant"
)

func TestBootstrapStoresOnlyDerivedKeyAndAuthenticates(t *testing.T) {
	raw := randomKey(t)
	store, err := Bootstrap(env(raw, `{"mock-model":["mock"]}`))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	h := Authenticate(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if id, ok := TenantID(r.Context()); !ok || id != "tenant_a" {
			t.Fatal("missing tenant context")
		}
	}))
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !called {
		t.Fatal("valid key rejected")
	}
	if _, ok := store.Route("tenant_a", "mock-model"); !ok {
		t.Fatal("missing route")
	}
}
func TestAuthRejectsBeforeHandler(t *testing.T) {
	raw := randomKey(t)
	store, err := Bootstrap(env(raw, `{"mock-model":["mock"]}`))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	h := Authenticate(store, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer "+randomKey(t))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if called || w.Code != http.StatusUnauthorized {
		t.Fatalf("called=%t code=%d", called, w.Code)
	}
}
func TestAuthRejectsDisabledKeyAndTenant(t *testing.T) {
	for _, test := range []struct {
		name        string
		keyEnabled  bool
		tenantAlive bool
	}{
		{"disabled key", false, true},
		{"disabled tenant", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := randomKey(t)
			store := tenant.NewMemory([]tenant.Tenant{{ID: "tenant_a", Enabled: test.tenantAlive}}, []tenant.APIKeyRecord{{Prefix: raw[:8], Hash: sha256.Sum256([]byte(raw)), TenantID: "tenant_a", Enabled: test.keyEnabled}})
			called := false
			h := Authenticate(store, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			r.Header.Set("Authorization", "Bearer "+raw)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if called || w.Code != http.StatusUnauthorized || w.Body.String() != "{\"error\":{\"code\":\"auth_failed\"}}\n" {
				t.Fatalf("called=%t status=%d body=%q", called, w.Code, w.Body.String())
			}
		})
	}
}
func TestBootstrapRejectsMissing(t *testing.T) {
	_, err := Bootstrap(func(string) string { return "" })
	code, ok := IsConfigurationError(err)
	if !ok || code != CodeConfigurationMissing {
		t.Fatalf("%v", err)
	}
}
func env(key, routes string) func(string) string {
	return func(name string) string {
		switch name {
		case "AGENTMESH_BOOTSTRAP_API_KEY":
			return key
		case "AGENTMESH_BOOTSTRAP_TENANT_ID":
			return "tenant_a"
		case "AGENTMESH_BOOTSTRAP_MODEL_ROUTES":
			return routes
		}
		return ""
	}
}
func randomKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
