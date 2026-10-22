package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/auth"
	"agentmesh/internal/billing"
	"agentmesh/internal/provider"
	"agentmesh/internal/reservation"
)

type moneyProbe struct {
	begins, starts, finishes, completes int
	receipts                            []provider.Usage
	beginErr                            error
}

func (p *moneyProbe) Begin(context.Context, string, string, []provider.Message) (reservation.StreamSession, error) {
	p.begins++
	if p.beginErr != nil {
		return nil, p.beginErr
	}
	return p, nil
}
func (p *moneyProbe) BillingID() string { return "01234567890123456789012345678901" }
func (p *moneyProbe) BeforeAttempt(context.Context, string, provider.ChatRequest) error {
	p.starts++
	return nil
}
func (p *moneyProbe) BeforeEmit(_ context.Context, _ string, c provider.Chunk) error {
	if c.Usage == nil {
		return errors.New("missing receipt")
	}
	p.receipts = append(p.receipts, *c.Usage)
	return nil
}
func (p *moneyProbe) AfterEmit(context.Context, string, provider.Chunk) error { return nil }
func (p *moneyProbe) FinishAttempt(context.Context, string, string) error     { p.finishes++; return nil }
func (p *moneyProbe) Complete(context.Context, string, bool) error            { p.completes++; return nil }

func TestMonetaryEmbeddingBatchesReceiptsAndHeader(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Input []string }
		_ = json.NewDecoder(r.Body).Decode(&input)
		rows := []map[string]any{}
		for i := range input.Input {
			rows = append(rows, map[string]any{"index": i, "embedding": []float64{1, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "cloud", "data": rows, "usage": map[string]int{"prompt_tokens": len(rows), "total_tokens": len(rows)}})
	}))
	defer s.Close()
	p, _ := NewCompatibleProvider(CompatibleConfig{BaseURL: s.URL, APIKey: "fixture", Model: "cloud", Dimension: 2})
	gate := &moneyProbe{}
	providers, bindings, err := EnableBilling([]Provider{p}, gate)
	if err != nil || bindings[0].UpstreamModel != "cloud" {
		t.Fatal(err)
	}
	key := "embedding-test-private-key"
	values := map[string]string{"AGENTMESH_BOOTSTRAP_API_KEY": key, "AGENTMESH_BOOTSTRAP_TENANT_ID": "tenant", "AGENTMESH_BOOTSTRAP_MODEL_ROUTES": `{"embedding":["compatible"]}`}
	store, _ := auth.Bootstrap(func(k string) string { return values[k] })
	h, _ := NewHandler(store, providers)
	inputs := make([]string, 21)
	for i := range inputs {
		inputs[i] = "text"
	}
	body, _ := json.Marshal(map[string]any{"model": "embedding", "input": inputs})
	r := httptest.NewRequest("POST", EmbeddingsPath, strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	auth.Authenticate(store, h).ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("X-AgentMesh-Billing-Request-ID") == "" || gate.begins != 1 || gate.starts != 2 || gate.completes != 1 || len(gate.receipts) != 2 || gate.receipts[0].InputTokens != 20 || gate.receipts[1].InputTokens != 1 {
		t.Fatalf("money batches: status=%d gate=%+v", w.Code, gate)
	}
	var result struct{ Usage responseUsage }
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Usage.Total != 21 {
		t.Fatal("aggregate usage")
	}
	// Semantic cache calls Provider.Embed directly, outside the HTTP handler.
	cacheCall := auth.Authenticate(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := providers[0].Embed(r.Context(), "actual-upstream-name", []string{"cache lookup"}); err != nil {
			t.Error(err)
		}
	}))
	r = httptest.NewRequest("POST", "/internal-cache", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	cacheCall.ServeHTTP(httptest.NewRecorder(), r)
	if gate.begins != 2 || gate.starts != 3 || gate.completes != 2 || len(gate.receipts) != 3 {
		t.Fatal("cache bypassed money gate")
	}
	gate.beginErr = billing.ErrInsufficient
	r = httptest.NewRequest("POST", EmbeddingsPath, strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+key)
	w = httptest.NewRecorder()
	auth.Authenticate(store, h).ServeHTTP(w, r)
	if w.Code != 402 || gate.starts != 3 {
		t.Fatal("insufficient balance called upstream")
	}
}

func TestMeteredEmbeddingMissingOrMalformedUsageIsNotInvented(t *testing.T) {
	for _, usage := range []string{`null`, `{}`, `{"prompt_tokens":-1,"total_tokens":-1}`, `{"prompt_tokens":2,"total_tokens":3}`} {
		t.Run(usage, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}],"usage":` + usage + `}`))
			}))
			defer s.Close()
			p, _ := NewCompatibleProvider(CompatibleConfig{BaseURL: s.URL, APIKey: "fixture", Model: "cloud", Dimension: 2})
			gate := &moneyProbe{}
			_, _, err := p.EmbedMetered(context.Background(), "embedding", []string{"x"}, gate)
			if !errors.Is(err, ErrProtocol) || len(gate.receipts) != 0 || gate.starts != 1 || gate.finishes != 1 {
				t.Fatalf("invented receipt: %v %+v", err, gate)
			}
		})
	}
}

func TestMeteredEmbeddingPartialFailureKeepsActualFirstReceipt(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(502)
			return
		}
		rows := []map[string]any{}
		for i := 0; i < 20; i++ {
			rows = append(rows, map[string]any{"index": i, "embedding": []float64{1, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": rows, "usage": map[string]int{"prompt_tokens": 100, "total_tokens": 100}})
	}))
	defer s.Close()
	p, _ := NewCompatibleProvider(CompatibleConfig{BaseURL: s.URL, APIKey: "fixture", Model: "cloud", Dimension: 2})
	input := make([]string, 21)
	for i := range input {
		input[i] = "x"
	}
	gate := &moneyProbe{}
	_, _, err := p.EmbedMetered(context.Background(), "embedding", input, gate)
	if !errors.Is(err, ErrUpstream) || gate.starts != 2 || gate.finishes != 2 || len(gate.receipts) != 1 || gate.receipts[0].InputTokens != 100 {
		t.Fatalf("lost first receipt: %v %+v", err, gate)
	}
}
