package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/reservation"
	"agentmesh/internal/tenant"
)

func TestDisabledReservationGateKeepsConcurrentAuthenticatedSSEFallback(t *testing.T) {
	gate, cleanup, err := reservation.OpenConfiguredCoordinator(func(string) string { return "" })
	if err != nil || gate != nil {
		t.Fatalf("disabled gate=%v err=%v", gate, err)
	}
	defer cleanup()

	raw := randomRawKey(t)
	store := tenant.NewMemory([]tenant.Tenant{{
		ID: "tenant_a", Enabled: true, ModelRoutes: map[string][]string{"mock-model": {"primary", "fallback"}},
	}}, []tenant.APIKeyRecord{record(raw, "tenant_a")})
	primary := provider.NewMock(provider.MockConfig{Name: "primary", FailBeforeFirst: true, FailAfterChunks: -1})
	fallback := provider.NewMock(provider.MockConfig{Name: "fallback", Chunks: []string{"fallback-ok"}, FailAfterChunks: -1})
	resolver, err := tenant.NewResolver(context.Background(), store, []string{"primary", "fallback"}, []provider.Provider{primary, fallback})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRoutingAndRecorderAndReservations(resolver, nil, gate)
	httpServer := httptest.NewServer(server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) }))
	defer httpServer.Close()

	var group sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			request, err := http.NewRequest(http.MethodPost, httpServer.URL+chatPath, bytes.NewBufferString(`{"model":"mock-model","messages":[{"role":"user","content":"hello"}],"stream":true}`))
			if err != nil {
				errors <- err
				return
			}
			request.Header.Set("Authorization", "Bearer "+raw)
			response, err := httpServer.Client().Do(request)
			if err != nil {
				errors <- err
				return
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				errors <- err
				return
			}
			if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(string(body), "fallback-ok") || !strings.Contains(string(body), "data: [DONE]") {
				errors <- fmt.Errorf("status=%d content_type=%q body=%q", response.StatusCode, response.Header.Get("Content-Type"), body)
			}
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if primary.Calls() != 2 || fallback.Calls() != 2 {
		t.Fatalf("primary=%d fallback=%d", primary.Calls(), fallback.Calls())
	}
}
