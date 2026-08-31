package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/auth"
	"agentmesh/internal/observability"
	"agentmesh/internal/provider"
	"agentmesh/internal/tenant"
)

// TestHTTPContractPublicAndTenantHealth records the public/protected split in
// specs/003-local-api-key-gate/contract.md without changing gateway behavior.
func TestHTTPContractPublicAndTenantHealth(t *testing.T) {
	raw, store, mock := authenticatedStore(t)
	resolver, err := tenant.NewResolver(store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRouting(resolver)
	handler := server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, healthPath, nil))
	if health.Code != http.StatusOK || health.Body.String() != `{"status":"ok"}` {
		t.Fatalf("health status=%d body=%q", health.Code, health.Body.String())
	}

	protected := httptest.NewRecorder()
	handler.ServeHTTP(protected, httptest.NewRequest(http.MethodGet, "/health/providers", nil))
	if protected.Code != http.StatusUnauthorized || protected.Body.String() != "{\"error\":{\"code\":\"auth_failed\"}}\n" || mock.Calls() != 0 {
		t.Fatalf("provider health status=%d body=%q calls=%d", protected.Code, protected.Body.String(), mock.Calls())
	}

	allowed := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health/providers", nil)
	request.Header.Set("Authorization", "Bearer "+raw)
	handler.ServeHTTP(allowed, request)
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), "mock-primary") {
		t.Fatalf("authorized provider health status=%d body=%q", allowed.Code, allowed.Body.String())
	}
}

// TestHTTPContractTraceHeaderPrecedesModelAuthorization locks the current
// ordering: strict JSON succeeds and creates the trace before model rejection.
func TestHTTPContractTraceHeaderPrecedesModelAuthorization(t *testing.T) {
	raw, store, mock := authenticatedStore(t)
	recorder := observability.NewRecorder(8, nil, traceIDs("contract-trace"))
	handler := observedHandler(t, store, mock, recorder)
	request := httptest.NewRequest(http.MethodPost, chatPath, bytes.NewBufferString(`{"model":"not-routed","messages":[{"role":"user","content":"x"}],"stream":true}`))
	request.Header.Set("Authorization", "Bearer "+raw)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), modelNotAllowed) || response.Header().Get(traceHeader) != "contract-trace" || mock.Calls() != 0 {
		t.Fatalf("status=%d header=%q body=%q calls=%d", response.Code, response.Header().Get(traceHeader), response.Body.String(), mock.Calls())
	}
}
