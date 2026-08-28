package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/auth"
	"agentmesh/internal/observability"
	"agentmesh/internal/provider"
	"agentmesh/internal/tenant"
)

func TestTraceQueryIsTenantScopedAndSafe(t *testing.T) {
	rawA, rawB := randomRawKey(t), randomRawKey(t)
	store := tenant.NewMemory([]tenant.Tenant{
		{ID: "tenant_a", Enabled: true, ModelRoutes: map[string][]string{"model-a": {"mock"}}},
		{ID: "tenant_b", Enabled: true, ModelRoutes: map[string][]string{"model-b": {"mock"}}},
	}, []tenant.APIKeyRecord{record(rawA, "tenant_a"), record(rawB, "tenant_b")})
	mock := provider.NewMock(provider.MockConfig{Name: "mock-safe", Chunks: []string{"visible only as SSE"}, FailAfterChunks: -1})
	handler := observedHandler(t, store, mock, observability.NewRecorder(8, nil, traceIDs("trace-a", "pending")))

	chat := httptest.NewRequest(http.MethodPost, chatPath, bytes.NewBufferString(`{"model":"model-a","messages":[{"role":"user","content":"secret prompt"}],"stream":true}`))
	chat.Header.Set("Authorization", "Bearer "+rawA)
	chatRecorder := httptest.NewRecorder()
	handler.ServeHTTP(chatRecorder, chat)
	traceID := chatRecorder.Header().Get(traceHeader)
	if chatRecorder.Code != http.StatusOK || traceID != "trace-a" || !strings.Contains(chatRecorder.Body.String(), "visible only as SSE") {
		t.Fatalf("chat status=%d trace=%q body=%s", chatRecorder.Code, traceID, chatRecorder.Body.String())
	}

	query := httptest.NewRequest(http.MethodGet, "/v1/observability/traces/"+traceID, nil)
	query.Header.Set("Authorization", "Bearer "+rawA)
	queryRecorder := httptest.NewRecorder()
	callsBeforeQuery := mock.Calls()
	handler.ServeHTTP(queryRecorder, query)
	if queryRecorder.Code != http.StatusOK || mock.Calls() != callsBeforeQuery || strings.Contains(queryRecorder.Body.String(), "secret prompt") || strings.Contains(queryRecorder.Body.String(), "visible only as SSE") || strings.Contains(queryRecorder.Body.String(), rawA) {
		t.Fatalf("query status=%d calls=%d body=%s", queryRecorder.Code, mock.Calls(), queryRecorder.Body.String())
	}
	var trace observability.Trace
	if err := json.Unmarshal(queryRecorder.Body.Bytes(), &trace); err != nil || trace.TraceID != traceID || trace.Model != "model-a" || trace.ResultCode != "success" || trace.UsageObserved || len(trace.Attempts) != 1 || trace.Attempts[0].Provider != "mock-safe" {
		t.Fatalf("trace=%+v err=%v", trace, err)
	}

	for _, test := range []struct {
		name string
		key  string
		id   string
	}{
		{"cross tenant", rawB, traceID},
		{"unknown", rawA, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/observability/traces/"+test.id, nil)
			request.Header.Set("Authorization", "Bearer "+test.key)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotFound || recorder.Body.String() != "{\"error\":{\"code\":\"trace_not_found\"}}\n" || mock.Calls() != callsBeforeQuery {
				t.Fatalf("status=%d body=%q calls=%d", recorder.Code, recorder.Body.String(), mock.Calls())
			}
		})
	}
}

func TestTraceQueryRejectsPendingAndUnauthenticated(t *testing.T) {
	raw, store, mock := authenticatedStore(t)
	recorder := observability.NewRecorder(8, nil, traceIDs("pending"))
	pending := recorder.Start("tenant_a", "mock-model")
	handler := observedHandler(t, store, mock, recorder)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/observability/traces/"+pending.TraceID(), nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Body.String() != "{\"error\":{\"code\":\"auth_failed\"}}\n" || mock.Calls() != 0 {
			t.Fatalf("status=%d body=%q calls=%d", response.Code, response.Body.String(), mock.Calls())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/observability/traces/"+pending.TraceID(), nil)
	request.Header.Set("Authorization", "Bearer "+raw)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":{\"code\":\"trace_not_found\"}}\n" || mock.Calls() != 0 {
		t.Fatalf("status=%d body=%q calls=%d", response.Code, response.Body.String(), mock.Calls())
	}
}

func observedHandler(t *testing.T, store *tenant.MemoryStore, mock provider.Provider, recorder *observability.Recorder) http.Handler {
	t.Helper()
	resolver, err := tenant.NewResolver(store, []string{"mock"}, []provider.Provider{mock})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRoutingAndRecorder(resolver, recorder)
	return server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })
}

func traceIDs(values ...string) func() string {
	index := 0
	return func() string {
		if index >= len(values) {
			return ""
		}
		value := values[index]
		index++
		return value
	}
}
