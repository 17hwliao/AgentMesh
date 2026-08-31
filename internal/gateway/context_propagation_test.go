package gateway

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/router"
	"agentmesh/internal/tenant"
)

func TestTenantReadsReceiveCancelledRequestContext(t *testing.T) {
	type marker struct{}
	requestContext, cancel := context.WithCancel(context.WithValue(context.Background(), marker{}, "request"))
	cancel()
	store := &contextStore{}
	resolver := &contextResolver{}
	server := NewWithTenantRouting(resolver)
	handler := server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })

	chat := chatRequestForTest().WithContext(requestContext)
	chat.Header.Set("Authorization", "Bearer 12345678-context-key")
	chatResponse := httptest.NewRecorder()
	handler.ServeHTTP(chatResponse, chat)
	if chatResponse.Code != http.StatusForbidden || !hasCancelledMarker(store.contexts[0], marker{}) || !hasCancelledMarker(resolver.chatContext, marker{}) {
		t.Fatalf("chat status=%d auth=%v route=%v", chatResponse.Code, store.contexts, resolver.chatContext)
	}

	health := httptest.NewRequest(http.MethodGet, "/health/providers", nil).WithContext(requestContext)
	health.Header.Set("Authorization", "Bearer 12345678-context-key")
	healthResponse := httptest.NewRecorder()
	handler.ServeHTTP(healthResponse, health)
	if healthResponse.Code != http.StatusOK || len(store.contexts) != 2 || !hasCancelledMarker(store.contexts[1], marker{}) || !hasCancelledMarker(resolver.healthContext, marker{}) {
		t.Fatalf("health status=%d auth=%v visibility=%v", healthResponse.Code, store.contexts, resolver.healthContext)
	}
}

func hasCancelledMarker(ctx context.Context, key any) bool {
	return ctx != nil && ctx.Err() != nil && ctx.Value(key) == "request"
}

type contextStore struct{ contexts []context.Context }

func (s *contextStore) Authenticate(ctx context.Context, _ string, _ [sha256.Size]byte) (tenant.Tenant, bool) {
	s.contexts = append(s.contexts, ctx)
	return tenant.Tenant{ID: "tenant-a", Enabled: true}, true
}
func (*contextStore) Route(context.Context, string, string) ([]string, bool) { return nil, false }

type contextResolver struct {
	chatContext   context.Context
	healthContext context.Context
}

func (r *contextResolver) Streamer(ctx context.Context, _, _ string) (router.Streamer, bool) {
	r.chatContext = ctx
	return nil, false
}
func (r *contextResolver) VisibleProviders(ctx context.Context, _ string) []provider.Provider {
	r.healthContext = ctx
	return nil
}

var _ tenant.Store = (*contextStore)(nil)
var _ TenantResolver = (*contextResolver)(nil)
