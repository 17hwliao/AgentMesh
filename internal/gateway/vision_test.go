package gateway

import (
	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/reservation"
	"agentmesh/internal/runtime"
	"agentmesh/internal/tenant"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type visionGate struct{ calls int }

func (g *visionGate) Begin(context.Context, string, string, []provider.Message) (reservation.StreamSession, error) {
	g.calls++
	return nil, errors.New("unexpected billing")
}
func TestVisionHTTPHistoryAndRejectionBeforeWallet(t *testing.T) {
	calls := 0
	msgs := []provider.Message{{Role: "system", Content: "system instruction"}, {Role: "user", Parts: []provider.ContentPart{{Type: "text", Text: "image question"}, {Type: "image_url", ImageURL: &provider.ImageURL{URL: "https://example.com/image.png"}}}}, {Role: "assistant", Content: "prior answer"}, {Role: "user", Content: "followup"}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var got provider.ChatRequest
		if json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(got.Messages, msgs) {
			t.Error("context changed")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"red\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":500,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	values := map[string]string{"COMPATIBLE_BASE_URL": upstream.URL, "COMPATIBLE_API_KEY": "fixture", "COMPATIBLE_MODELS": `{"v":"actual-vision","text":"actual-text"}`, "COMPATIBLE_VISION_MODELS": `["v"]`}
	ps, e := runtime.Build("compatible", func(k string) string { return values[k] })
	if e != nil {
		t.Fatal(e)
	}
	key := randomRawKey(t)
	store := tenant.NewMemory([]tenant.Tenant{{ID: "viewer", Enabled: true, ModelRoutes: map[string][]string{"v": {"compatible"}, "text": {"compatible"}}}}, []tenant.APIKeyRecord{record(key, "viewer")})
	resolver, e := tenant.NewResolver(context.Background(), store, []string{"compatible"}, ps)
	if e != nil {
		t.Fatal(e)
	}
	call := func(s *Server, model string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"model": model, "messages": msgs, "stream": true, "max_tokens": 32})
		r := httptest.NewRequest("POST", chatPath, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		s.AuthenticatedHandler(func(h http.Handler) http.Handler { return auth.Authenticate(store, h) }).ServeHTTP(w, r)
		return w
	}
	if w := call(NewWithTenantRouting(resolver), "v"); w.Code != 200 || !strings.Contains(w.Body.String(), "red") || calls != 1 {
		t.Fatal("vision HTTP failed", w.Code)
	}
	gate := &visionGate{}
	if w := call(NewWithTenantRoutingAndRecorderAndReservations(resolver, nil, gate), "text"); w.Code != 400 || !strings.Contains(w.Body.String(), "unsupported_model_input") || gate.calls != 0 || calls != 1 {
		t.Fatal("unsupported images reached wallet or upstream")
	}
}
