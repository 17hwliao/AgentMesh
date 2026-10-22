package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatibleEmbeddingPinnedBatchDimensionAndOrder(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input struct {
			Model      string
			Input      []string
			Dimensions int
			Encoding   string `json:"encoding_format"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "cloud-embedding" || input.Dimensions != 2 || input.Encoding != "float" || len(input.Input) > 20 || r.Header.Get("Authorization") != "Bearer private-fixture" || r.URL.Path != "/v1/embeddings" {
			t.Error("upstream contract")
		}
		rows := []map[string]any{}
		for i := len(input.Input) - 1; i >= 0; i-- {
			rows = append(rows, map[string]any{"index": i, "embedding": []float64{float64(i), 1}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "cloud-embedding", "data": rows})
	}))
	defer server.Close()
	p, err := NewCompatibleProvider(CompatibleConfig{BaseURL: server.URL + "/v1", APIKey: "private-fixture", Model: "cloud-embedding", Dimension: 2})
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]string, 32)
	for i := range inputs {
		inputs[i] = "text"
	}
	vectors, err := p.Embed(context.Background(), "public-alias-must-not-replace-model", inputs)
	if err != nil || len(vectors) != 32 || vectors[19][0] != 19 || vectors[20][0] != 0 || calls != 2 {
		t.Fatalf("batch/order failure: %v", err)
	}
}

func TestCompatibleEmbeddingRejectsProtocolAndSanitizesErrors(t *testing.T) {
	for _, body := range []string{
		`{"data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[1,0]}]}`,
		`{"data":[{"index":0,"embedding":[1]}]}`,
		`{"model":"wrong-model","data":[{"index":0,"embedding":[1,0]}]}`,
		`{"data":[{"index":1,"embedding":[1,0]}]}`,
		`{"data":[{"index":0,"embedding":[1e999,0]}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer s.Close()
			p, _ := NewCompatibleProvider(CompatibleConfig{BaseURL: s.URL, APIKey: "private-fixture", Model: "model", Dimension: 2})
			if _, err := p.Embed(context.Background(), "alias", []string{"text"}); !errors.Is(err, ErrProtocol) {
				t.Fatalf("expected protocol error, got %v", err)
			}
		})
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("private-fixture provider details"))
	}))
	defer s.Close()
	p, _ := NewCompatibleProvider(CompatibleConfig{BaseURL: s.URL, APIKey: "private-fixture", Model: "model", Dimension: 2})
	if _, err := p.Embed(context.Background(), "alias", []string{"text"}); !errors.Is(err, ErrUpstream) || strings.Contains(err.Error(), "private-fixture") {
		t.Fatal("upstream error leaked")
	}
}

func TestCompatibleEmbeddingRedirectCancelAndRuntime(t *testing.T) {
	forwarded := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++ }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer s.Close()
	values := map[string]string{"AGENTMESH_EMBEDDING_PROVIDER": "compatible", "AGENTMESH_EMBEDDING_BASE_URL": s.URL, "AGENTMESH_EMBEDDING_MODEL": "cloud-model", "AGENTMESH_EMBEDDING_API_KEY": "private-fixture", "AGENTMESH_EMBEDDING_DIMENSION": "2"}
	providers, err := Build(func(k string) string { return values[k] })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = providers[0].Embed(context.Background(), "alias", []string{"text"}); !errors.Is(err, ErrUpstream) || forwarded != 0 {
		t.Fatal("redirect followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = providers[0].Embed(ctx, "alias", []string{"text"}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel lost")
	}
	for _, key := range []string{"AGENTMESH_EMBEDDING_API_KEY", "AGENTMESH_EMBEDDING_DIMENSION"} {
		old := values[key]
		values[key] = ""
		if _, err = Build(func(k string) string { return values[k] }); err == nil {
			t.Fatal("missing config accepted")
		}
		values[key] = old
	}
}
