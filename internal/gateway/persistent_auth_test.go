package gateway

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"agentmesh/internal/admin"
	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/tenant"
)

func TestPersistentModeRevocationRejectsNextRequestBeforeProvider(t *testing.T) {
	raw := "12345678-persisted-key-material"
	store := &revocableStore{raw: raw, enabled: true}
	mock := provider.NewMock(provider.MockConfig{Name: "mock-primary", Chunks: []string{"ok"}, FailAfterChunks: -1})
	resolver, err := tenant.NewResolver(store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRouting(resolver)
	protected := server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })
	root := http.NewServeMux()
	root.Handle("/admin/", admin.NewHandler(revokingLifecycle{store: store}, sha256.Sum256([]byte("admin-token")), func(route []string) bool { return len(route) == 1 && route[0] == "mock" }))
	root.Handle("/", protected)

	chat := chatRequestForTest()
	chat.Header.Set("Authorization", "Bearer "+raw)
	first := httptest.NewRecorder()
	root.ServeHTTP(first, chat)
	if first.Code != http.StatusOK || mock.Calls() != 1 {
		t.Fatalf("first status=%d calls=%d", first.Code, mock.Calls())
	}

	revoke := httptest.NewRequest(http.MethodDelete, "/admin/api-keys/key-id", nil)
	revoke.Header.Set("Authorization", "Bearer admin-token")
	revoked := httptest.NewRecorder()
	root.ServeHTTP(revoked, revoke)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%q", revoked.Code, revoked.Body.String())
	}

	blocked := chatRequestForTest()
	blocked.Header.Set("Authorization", "Bearer "+raw)
	response := httptest.NewRecorder()
	root.ServeHTTP(response, blocked)
	if response.Code != http.StatusUnauthorized || mock.Calls() != 1 {
		t.Fatalf("blocked status=%d calls=%d", response.Code, mock.Calls())
	}
}

type revocableStore struct {
	mu      sync.RWMutex
	raw     string
	enabled bool
}

func (s *revocableStore) Authenticate(prefix string, digest [sha256.Size]byte) (tenant.Tenant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if prefix != s.raw[:8] || digest != sha256.Sum256([]byte(s.raw)) || !s.enabled {
		return tenant.Tenant{}, false
	}
	return tenant.Tenant{ID: "tenant-a", Enabled: true}, true
}
func (*revocableStore) Route(tenantID, model string) ([]string, bool) {
	if tenantID != "tenant-a" || model != "mock-model" {
		return nil, false
	}
	return []string{"mock"}, true
}
func (*revocableStore) Routes(string) [][]string { return [][]string{{"mock"}} }
func (*revocableStore) AllRoutes() [][]string    { return [][]string{{"mock"}} }

type revokingLifecycle struct{ store *revocableStore }

func (r revokingLifecycle) CreateTenant(context.Context, tenant.Tenant) error { return nil }
func (r revokingLifecycle) CreateAPIKey(context.Context, string) (tenant.APIKey, string, error) {
	return tenant.APIKey{}, "", nil
}
func (r revokingLifecycle) RevokeAPIKey(context.Context, string) error {
	r.store.mu.Lock()
	r.store.enabled = false
	r.store.mu.Unlock()
	return nil
}
