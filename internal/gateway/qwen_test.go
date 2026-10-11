package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/runtime"
	"agentmesh/internal/tenant"
)

func TestQwenGatewayAliasesAndFailureBeforeAnswer(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input provider.ChatRequest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "qwen-plus" || r.Header.Get("Authorization") != "Bearer upstream-private-key" {
			t.Error("incorrect upstream model or credential")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
		if input.Messages[0].Content == "fail" {
			fmt.Fprint(w, "data: {\"error\":{\"message\":\"private upstream details\"}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	values := map[string]string{"COMPATIBLE_BASE_URL": upstream.URL, "COMPATIBLE_API_KEY": "upstream-private-key", "COMPATIBLE_MODELS": `{"sql-review":"qwen-plus","domestic-chat":"qwen-plus"}`, "COMPATIBLE_DISABLE_THINKING": "true"}
	providers, err := runtime.Build("compatible", func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	key := randomRawKey(t)
	store := tenant.NewMemory([]tenant.Tenant{{ID: "operator", Enabled: true, ModelRoutes: map[string][]string{"sql-review": {"compatible"}, "domestic-chat": {"compatible"}}}}, []tenant.APIKeyRecord{record(key, "operator")})
	resolver, err := tenant.NewResolver(context.Background(), store, []string{"compatible"}, providers)
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithTenantRouting(resolver).AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })
	for _, tc := range []struct {
		model, text, token string
		status             int
		attempts           int32
	}{
		{"sql-review", "hello", "", http.StatusUnauthorized, 0},
		{"unmapped", "hello", key, http.StatusForbidden, 0},
		{"sql-review", "hello", key, http.StatusOK, 1},
		{"domestic-chat", "hello", key, http.StatusOK, 2},
		{"sql-review", "fail", key, http.StatusServiceUnavailable, 3},
	} {
		r := httptest.NewRequest(http.MethodPost, chatPath, strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"stream":true,"max_tokens":32}`, tc.model, tc.text)))
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != tc.status || calls.Load() != tc.attempts {
			t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), body)
		}
		if strings.Contains(body, "private") {
			t.Fatal("upstream details leaked")
		}
		if tc.status == http.StatusOK && (!strings.Contains(body, "OK") || !strings.Contains(body, "[DONE]")) {
			t.Fatal("incomplete gateway stream")
		}
	}
}
