package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/reservation"
	"agentmesh/internal/tenant"
)

func TestAuthRejectsBeforeBodyDecodeAndProviderAttempt(t *testing.T) {
	raw, store, mock := authenticatedStore(t)
	_ = raw
	server := newAuthenticatedServer(t, store, mock, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, chatPath, strings.NewReader(`not json`))
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || recorder.Body.String() != "{\"error\":{\"code\":\"auth_failed\"}}\n" || mock.Calls() != 0 {
		t.Fatalf("status=%d body=%q calls=%d", recorder.Code, recorder.Body.String(), mock.Calls())
	}
}

func TestTenantModelRouteAndProviderHealthAreIsolated(t *testing.T) {
	rawA := randomRawKey(t)
	rawB := randomRawKey(t)
	store := tenant.NewMemory([]tenant.Tenant{
		{ID: "tenant_a", Enabled: true, ModelRoutes: map[string][]string{"model-a": {"mock"}}},
		{ID: "tenant_b", Enabled: true, ModelRoutes: map[string][]string{"model-b": {"mock"}}},
	}, []tenant.APIKeyRecord{record(rawA, "tenant_a"), record(rawB, "tenant_b")})
	mock := provider.NewMock(provider.MockConfig{Name: "mock-visible", Chunks: []string{"ok"}, FailAfterChunks: -1})
	server := newAuthenticatedServer(t, store, mock, nil)

	blocked := httptest.NewRequest(http.MethodPost, chatPath, bytes.NewBufferString(`{"model":"model-b","messages":[{"role":"user","content":"x"}],"stream":true}`))
	blocked.Header.Set("Authorization", "Bearer "+rawA)
	blockedRecorder := httptest.NewRecorder()
	server.ServeHTTP(blockedRecorder, blocked)
	if blockedRecorder.Code != http.StatusForbidden || !strings.Contains(blockedRecorder.Body.String(), modelNotAllowed) || mock.Calls() != 0 {
		t.Fatalf("status=%d body=%s calls=%d", blockedRecorder.Code, blockedRecorder.Body.String(), mock.Calls())
	}

	health := httptest.NewRequest(http.MethodGet, "/health/providers", nil)
	health.Header.Set("Authorization", "Bearer "+rawA)
	healthRecorder := httptest.NewRecorder()
	server.ServeHTTP(healthRecorder, health)
	if healthRecorder.Code != http.StatusOK || !strings.Contains(healthRecorder.Body.String(), "mock-visible") || strings.Contains(healthRecorder.Body.String(), "tenant_a") {
		t.Fatalf("health=%d body=%s", healthRecorder.Code, healthRecorder.Body.String())
	}
}

func TestQuotaRejectionHasNoProviderAttempt(t *testing.T) {
	raw, store, mock := authenticatedStore(t)
	server := newAuthenticatedServer(t, store, mock, rejectQuota{})
	request := chatRequestForTest()
	request.Header.Set("Authorization", "Bearer "+raw)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), quotaExhausted) || mock.Calls() != 0 {
		t.Fatalf("status=%d body=%s calls=%d", recorder.Code, recorder.Body.String(), mock.Calls())
	}
}

func TestReservationGateRejectsBeforeProviderAttempt(t *testing.T) {
	raw, store, mock := authenticatedStore(t)
	resolver, err := tenant.NewResolver(store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRoutingAndRecorderAndReservations(resolver, nil, rejectingReservationGate{}, nil)
	request := chatRequestForTest()
	request.Header.Set("Authorization", "Bearer "+raw)
	recorder := httptest.NewRecorder()
	server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) }).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), quotaExhausted) || mock.Calls() != 0 {
		t.Fatalf("status=%d body=%s calls=%d", recorder.Code, recorder.Body.String(), mock.Calls())
	}
}

func TestReservationStorageFailureHasNoProviderAttempt(t *testing.T) {
	raw, store, mock := authenticatedStore(t)
	resolver, err := tenant.NewResolver(store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRoutingAndRecorderAndReservations(resolver, nil, unavailableReservationGate{}, nil)
	request := chatRequestForTest()
	request.Header.Set("Authorization", "Bearer "+raw)
	recorder := httptest.NewRecorder()
	server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) }).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), quotaUnavailable) || mock.Calls() != 0 {
		t.Fatalf("status=%d body=%s calls=%d", recorder.Code, recorder.Body.String(), mock.Calls())
	}
}

type rejectQuota struct{}

func (rejectQuota) Allow(context.Context, string, string) bool { return false }

type rejectingReservationGate struct{}

func (rejectingReservationGate) Begin(context.Context, string, string, []provider.Message) (reservation.StreamSession, error) {
	return nil, &reservation.Error{Code: quotaExhausted}
}

type unavailableReservationGate struct{}

func (unavailableReservationGate) Begin(context.Context, string, string, []provider.Message) (reservation.StreamSession, error) {
	return nil, errors.New("storage unavailable")
}

func authenticatedStore(t *testing.T) (string, *tenant.MemoryStore, *provider.Mock) {
	t.Helper()
	raw := randomRawKey(t)
	store := tenant.NewMemory([]tenant.Tenant{{ID: "tenant_a", Enabled: true, ModelRoutes: map[string][]string{"mock-model": {"mock"}}}}, []tenant.APIKeyRecord{record(raw, "tenant_a")})
	mock := provider.NewMock(provider.MockConfig{Name: "mock-primary", Chunks: []string{"ok"}, FailAfterChunks: -1})
	return raw, store, mock
}

func newAuthenticatedServer(t *testing.T, store *tenant.MemoryStore, mock provider.Provider, quota QuotaGate) http.Handler {
	t.Helper()
	resolver, err := tenant.NewResolver(store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRouting(resolver, quota)
	return server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })
}

func record(raw, tenantID string) tenant.APIKeyRecord {
	return tenant.APIKeyRecord{Prefix: raw[:8], Hash: sha256.Sum256([]byte(raw)), TenantID: tenantID, Enabled: true}
}

func randomRawKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
