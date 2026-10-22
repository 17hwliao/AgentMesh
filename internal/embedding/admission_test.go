package embedding

import (
	"agentmesh/internal/auth"
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockedEmbedding struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockedEmbedding) Name() string { return "compatible" }
func (p *blockedEmbedding) Embed(ctx context.Context, _ string, _ []string) ([][]float64, error) {
	p.started <- struct{}{}
	select {
	case <-p.release:
		return [][]float64{{1, 0}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestEmbeddingAdmissionRejectsNinthAndRecovers(t *testing.T) {
	key := "embedding-concurrency-private-fixture"
	values := map[string]string{"AGENTMESH_BOOTSTRAP_API_KEY": key, "AGENTMESH_BOOTSTRAP_TENANT_ID": "tenant", "AGENTMESH_BOOTSTRAP_MODEL_ROUTES": `{"embedding":["compatible"]}`}
	store, err := auth.Bootstrap(func(k string) string { return values[k] })
	if err != nil {
		t.Fatal(err)
	}
	p := &blockedEmbedding{make(chan struct{}, 9), make(chan struct{})}
	h, _ := NewHandler(store, []Provider{p})
	protected := auth.Authenticate(store, h)
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", EmbeddingsPath, strings.NewReader(`{"model":"embedding","input":"text"}`))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		return w
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if call().Code != 200 {
				t.Error("allowed request failed")
			}
		}()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-p.started:
		case <-time.After(3 * time.Second):
			close(p.release)
			wg.Wait()
			t.Fatal("requests did not reach embedding provider")
		}
	}
	if w := call(); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("admission not bounded")
	}
	close(p.release)
	wg.Wait()
	if call().Code != 200 {
		t.Fatal("admission did not recover")
	}
}
