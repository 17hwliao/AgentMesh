package admin

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/tenant"
)

func TestAdminRejectsInvalidTokenBeforeReadingInvalidBody(t *testing.T) {
	lifecycle := &fakeLifecycle{}
	handler := NewHandler(lifecycle, sha256.Sum256([]byte("admin-token")), allowMock)
	request := httptest.NewRequest(http.MethodPost, "/admin/tenants", strings.NewReader(`{invalid json`))
	request.Header.Set("Authorization", "Bearer wrong-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Body.String() != "{\"error\":{\"code\":\"admin_auth_failed\"}}\n" || lifecycle.calls != 0 {
		t.Fatalf("status=%d body=%q calls=%d", response.Code, response.Body.String(), lifecycle.calls)
	}
}

func TestAdminCreatesKeyWithSingleRawKeyResponse(t *testing.T) {
	raw := "12345678-one-time-raw-key"
	lifecycle := &fakeLifecycle{key: tenant.APIKey{ID: "key-id"}, raw: raw}
	handler := NewHandler(lifecycle, sha256.Sum256([]byte("admin-token")), allowMock)
	request := httptest.NewRequest(http.MethodPost, "/admin/api-keys", strings.NewReader(`{"tenant_id":"tenant-a"}`))
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Body.String() != "{\"api_key\":\""+raw+"\",\"key_id\":\"key-id\"}\n" || lifecycle.calls != 1 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d body=%q calls=%d cache-control=%q", response.Code, response.Body.String(), lifecycle.calls, response.Header().Get("Cache-Control"))
	}
}

func TestAdminRejectsRouteOutsideConfiguredProviderMode(t *testing.T) {
	lifecycle := &fakeLifecycle{}
	handler := NewHandler(lifecycle, sha256.Sum256([]byte("admin-token")), allowMock)
	request := httptest.NewRequest(http.MethodPost, "/admin/tenants", strings.NewReader(`{"tenant_id":"tenant-a","model_routes":{"model-a":["ark"]}}`))
	request.Header.Set("Authorization", "Bearer admin-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || lifecycle.calls != 0 {
		t.Fatalf("status=%d calls=%d", response.Code, lifecycle.calls)
	}
}

func TestAdminRevokeIsIdempotent(t *testing.T) {
	lifecycle := &fakeLifecycle{}
	handler := NewHandler(lifecycle, sha256.Sum256([]byte("admin-token")), allowMock)
	for range 2 {
		request := httptest.NewRequest(http.MethodDelete, "/admin/api-keys/key-id", nil)
		request.Header.Set("Authorization", "Bearer admin-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
		}
	}
	if lifecycle.revocations != 2 {
		t.Fatalf("revocations=%d", lifecycle.revocations)
	}
}

type fakeLifecycle struct {
	calls       int
	revocations int
	key         tenant.APIKey
	raw         string
}

func (f *fakeLifecycle) CreateTenant(context.Context, tenant.Tenant) error { f.calls++; return nil }
func (f *fakeLifecycle) CreateAPIKey(context.Context, string) (tenant.APIKey, string, error) {
	f.calls++
	return f.key, f.raw, nil
}
func (f *fakeLifecycle) RevokeAPIKey(context.Context, string) error { f.revocations++; return nil }

func allowMock(route []string) bool { return len(route) == 1 && route[0] == "mock" }
