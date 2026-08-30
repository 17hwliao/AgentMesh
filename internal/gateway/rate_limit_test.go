package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/ratelimit"
	"agentmesh/internal/reservation"
	"agentmesh/internal/router"
	"agentmesh/internal/tenant"
)

func TestRateLimitRejectsAfterAuthBeforeBodyOrDownstream(t *testing.T) {
	raw, store, mock := authenticatedStore(t)
	resolver := &countingResolver{streamer: router.New(mock)}
	quota := &countingQuota{}
	reservations := &countingReservation{}
	server := NewWithTenantRoutingAndRecorderAndReservations(resolver, nil, reservations, quota)
	gate := &countingRateGate{decision: ratelimit.Decision{RetryAfter: time.Minute}}
	server.SetRateGate(gate)
	handler := server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })

	unauthorizedBody := &trackingBody{Reader: strings.NewReader(`not json`)}
	unauthorized := httptest.NewRequest(http.MethodPost, chatPath, unauthorizedBody)
	unauthorizedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized || unauthorizedBody.read || gate.calls != 0 {
		t.Fatalf("auth status=%d read=%t rate_calls=%d", unauthorizedRecorder.Code, unauthorizedBody.read, gate.calls)
	}

	body := &trackingBody{Reader: strings.NewReader(`not json`)}
	request := httptest.NewRequest(http.MethodPost, chatPath, body)
	request.Header.Set("Authorization", "Bearer "+raw)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), rateLimited) || recorder.Header().Get("Retry-After") != "60" {
		t.Fatalf("status=%d retry=%q body=%s", recorder.Code, recorder.Header().Get("Retry-After"), recorder.Body.String())
	}
	if body.read || gate.calls != 1 || resolver.calls != 0 || quota.calls != 0 || reservations.calls != 0 || mock.Calls() != 0 {
		t.Fatalf("read=%t rate=%d resolver=%d quota=%d reservations=%d provider=%d", body.read, gate.calls, resolver.calls, quota.calls, reservations.calls, mock.Calls())
	}
}

func TestRateLimitSeparatesTenantBuckets(t *testing.T) {
	rawA, rawB := randomRawKey(t), randomRawKey(t)
	store := tenant.NewMemory([]tenant.Tenant{
		{ID: "tenant-a", Enabled: true, ModelRoutes: map[string][]string{"mock-model": {"mock"}}},
		{ID: "tenant-b", Enabled: true, ModelRoutes: map[string][]string{"mock-model": {"mock"}}},
	}, []tenant.APIKeyRecord{record(rawA, "tenant-a"), record(rawB, "tenant-b")})
	mock := provider.NewMock(provider.MockConfig{Name: "mock", Chunks: []string{"ok"}, FailAfterChunks: -1})
	resolver, err := tenant.NewResolver(store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	gate, err := ratelimit.New(ratelimit.Config{PerMinute: 1, Burst: 1}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRouting(resolver)
	server.SetRateGate(gate)
	handler := server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })
	for _, test := range []struct {
		key  string
		want int
	}{
		{rawA, http.StatusOK},
		{rawA, http.StatusTooManyRequests},
		{rawB, http.StatusOK},
	} {
		request := chatRequestForTest()
		request.Header.Set("Authorization", "Bearer "+test.key)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("key request status=%d want=%d body=%s", recorder.Code, test.want, recorder.Body.String())
		}
	}
	if mock.Calls() != 2 {
		t.Fatalf("provider calls=%d", mock.Calls())
	}
}

type trackingBody struct {
	*strings.Reader
	read bool
}

func (b *trackingBody) Read(values []byte) (int, error) {
	b.read = true
	return b.Reader.Read(values)
}
func (b *trackingBody) Close() error { return nil }

type countingRateGate struct {
	calls    int
	decision ratelimit.Decision
}

func (g *countingRateGate) Admit(string) ratelimit.Decision {
	g.calls++
	return g.decision
}

type countingResolver struct {
	streamer router.Streamer
	calls    int
}

func (r *countingResolver) Streamer(string, string) (router.Streamer, bool) {
	r.calls++
	return r.streamer, true
}
func (r *countingResolver) VisibleProviders(string) []provider.Provider { return nil }

type countingQuota struct{ calls int }

func (g *countingQuota) Allow(context.Context, string, string) bool {
	g.calls++
	return true
}

type countingReservation struct{ calls int }

func (g *countingReservation) Begin(context.Context, string, string, []provider.Message) (reservation.StreamSession, error) {
	g.calls++
	return nil, nil
}

var _ TenantResolver = (*countingResolver)(nil)
var _ QuotaGate = (*countingQuota)(nil)
var _ reservation.StreamGate = (*countingReservation)(nil)
