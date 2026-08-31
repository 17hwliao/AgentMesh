package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/admin"
	"agentmesh/internal/admindemo"
	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/tenant"
)

func TestPristineIdentityBootstrapLetsAdminCreateFirstUsableKey(t *testing.T) {
	store := admindemo.NewStore()
	mock := provider.NewMock(provider.MockConfig{Name: "bootstrap-mock", Chunks: []string{"ready"}, FailAfterChunks: -1})
	resolver, err := tenant.NewResolver(context.Background(), store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		t.Fatalf("pristine resolver: %v", err)
	}
	root := http.NewServeMux()
	root.Handle("/admin/", admin.NewHandler(store, sha256.Sum256([]byte("bootstrap-admin")), func(route []string) bool {
		return tenant.RouteAllowed(route, []string{"mock"})
	}))
	server := NewWithTenantRouting(resolver)
	root.Handle("/", server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) }))

	unauthorizedBody := &trackingBody{Reader: strings.NewReader(`not json`)}
	unauthorized := httptest.NewRequest(http.MethodPost, chatPath, unauthorizedBody)
	unauthorized.Header.Set("Authorization", "Bearer 12345678-no-key")
	blocked := httptest.NewRecorder()
	root.ServeHTTP(blocked, unauthorized)
	if blocked.Code != http.StatusUnauthorized || unauthorizedBody.read || mock.Calls() != 0 {
		t.Fatalf("before bootstrap status=%d body_read=%t provider=%d", blocked.Code, unauthorizedBody.read, mock.Calls())
	}

	createTenant := httptest.NewRequest(http.MethodPost, "/admin/tenants", bytes.NewBufferString(`{"tenant_id":"first-tenant","model_routes":{"mock-model":["mock"]}}`))
	createTenant.Header.Set("Authorization", "Bearer bootstrap-admin")
	createdTenant := httptest.NewRecorder()
	root.ServeHTTP(createdTenant, createTenant)
	if createdTenant.Code != http.StatusCreated {
		t.Fatalf("create tenant status=%d body=%q", createdTenant.Code, createdTenant.Body.String())
	}

	createKey := httptest.NewRequest(http.MethodPost, "/admin/api-keys", bytes.NewBufferString(`{"tenant_id":"first-tenant"}`))
	createKey.Header.Set("Authorization", "Bearer bootstrap-admin")
	createdKey := httptest.NewRecorder()
	root.ServeHTTP(createdKey, createKey)
	var key struct {
		Raw string `json:"api_key"`
	}
	if createdKey.Code != http.StatusCreated || json.Unmarshal(createdKey.Body.Bytes(), &key) != nil || key.Raw == "" {
		t.Fatalf("create key status=%d body=%q", createdKey.Code, createdKey.Body.String())
	}

	chat := chatRequestForTest()
	chat.Header.Set("Authorization", "Bearer "+key.Raw)
	response := httptest.NewRecorder()
	root.ServeHTTP(response, chat)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ready") || mock.Calls() != 1 {
		t.Fatalf("after bootstrap status=%d body=%q provider=%d", response.Code, response.Body.String(), mock.Calls())
	}
}
