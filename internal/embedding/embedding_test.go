package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/auth"
)

func TestMockEmbeddingIsDeterministicAndBounded(t *testing.T) {
	provider, err := NewMockProvider(16)
	if err != nil {
		t.Fatal(err)
	}
	left, err := provider.Embed(context.Background(), "demo-model", []string{"same text", "second text"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := provider.Embed(context.Background(), "demo-model", []string{"same text", "second text"})
	if err != nil {
		t.Fatal(err)
	}
	if !equalVectors(left, right) || len(left) != 2 || len(left[0]) != 16 {
		t.Fatalf("mock output is not stable: left=%v right=%v", left, right)
	}
}

func TestOllamaAdapterContractAndControlledConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "nomic-embed" || len(request.Input) != 2 {
			t.Fatalf("unexpected body: %+v err=%v", request, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[1,0,0],[0,1,0]]}`))
	}))
	defer server.Close()
	provider, err := NewOllamaProvider(OllamaConfig{BaseURL: server.URL, Model: "nomic-embed"})
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := provider.Embed(context.Background(), "logical-model", []string{"a", "b"})
	if err != nil || len(vectors) != 2 || len(vectors[0]) != 3 {
		t.Fatalf("unexpected Ollama result: vectors=%v err=%v", vectors, err)
	}
	if _, err := NewOllamaProvider(OllamaConfig{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing Ollama config should refuse, got %v", err)
	}
}

func TestEmbeddingHandlerAuthTenantRouteLimitsAndTrace(t *testing.T) {
	key := "embedding-demo-key-123456"
	values := map[string]string{
		"AGENTMESH_BOOTSTRAP_API_KEY":      key,
		"AGENTMESH_BOOTSTRAP_TENANT_ID":    "tenant_a",
		"AGENTMESH_BOOTSTRAP_MODEL_ROUTES": `{"embed-model":["mock"]}`,
	}
	store, err := auth.Bootstrap(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewMockProvider(8)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store, []Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	protected := auth.Authenticate(store, handler)

	unauthorized := httptest.NewRecorder()
	protected.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, EmbeddingsPath, strings.NewReader(`{"model":"embed-model","input":"hello"}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, EmbeddingsPath, bytes.NewBufferString(`{"model":"embed-model","input":["hello","world"]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("X-AgentMesh-Trace-ID") == "" {
		t.Fatalf("authorized status=%d trace=%q body=%s", recorder.Code, recorder.Header().Get("X-AgentMesh-Trace-ID"), recorder.Body.String())
	}
	// This is the same minimal shape consumed by SQL Sentinel's
	// OpenAI-compatible HTTP embedding adapter: data[index].embedding.
	var clientShape struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &clientShape); err != nil || len(clientShape.Data) != 2 || len(clientShape.Data[0].Embedding) != 8 || clientShape.Data[0].Index != 0 {
		t.Fatalf("response is not OpenAI-compatible: %+v err=%v", clientShape, err)
	}
	var decoded response
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil || len(decoded.Data) != 2 || decoded.Provider != "mock" || decoded.Dimension != 8 {
		t.Fatalf("unexpected embedding response=%+v err=%v", decoded, err)
	}
	if strings.Contains(recorder.Body.String(), "hello") || strings.Contains(recorder.Body.String(), "world") {
		t.Fatal("embedding response leaked input text")
	}

	wrongModel := httptest.NewRequest(http.MethodPost, EmbeddingsPath, bytes.NewBufferString(`{"model":"not-allowed","input":"hello"}`))
	wrongModel.Header.Set("Authorization", "Bearer "+key)
	wrongResponse := httptest.NewRecorder()
	protected.ServeHTTP(wrongResponse, wrongModel)
	if wrongResponse.Code != http.StatusForbidden {
		t.Fatalf("wrong model status=%d body=%s", wrongResponse.Code, wrongResponse.Body.String())
	}

	unknownField := httptest.NewRequest(http.MethodPost, EmbeddingsPath, bytes.NewBufferString(`{"model":"embed-model","input":"hello","secret":"x"}`))
	unknownField.Header.Set("Authorization", "Bearer "+key)
	badResponse := httptest.NewRecorder()
	protected.ServeHTTP(badResponse, unknownField)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", badResponse.Code, badResponse.Body.String())
	}
}

func TestEmbeddingHandlerProviderFailureIsControlled(t *testing.T) {
	key := "embedding-demo-key-123456"
	store, err := auth.Bootstrap(func(name string) string {
		return map[string]string{
			"AGENTMESH_BOOTSTRAP_API_KEY":      key,
			"AGENTMESH_BOOTSTRAP_TENANT_ID":    "tenant_a",
			"AGENTMESH_BOOTSTRAP_MODEL_ROUTES": `{"embed-model":["ollama"]}`,
		}[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(store, []Provider{failingProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, EmbeddingsPath, strings.NewReader(`{"model":"embed-model","input":"hello"}`))
	request.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	auth.Authenticate(store, handler).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "embedding_provider_unavailable") {
		t.Fatalf("failure mapping status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBuildEmbeddingProviderIsExplicitForOllama(t *testing.T) {
	providers, err := Build(func(string) string { return "" })
	if err != nil || len(providers) != 1 || providers[0].Name() != "mock" {
		t.Fatalf("default build should be offline mock: providers=%v err=%v", providers, err)
	}
	missing, err := Build(func(name string) string {
		if name == "AGENTMESH_EMBEDDING_PROVIDER" {
			return "ollama"
		}
		return ""
	})
	if missing != nil || !errors.Is(err, &ConfigurationError{Code: CodeConfigurationMissing}) {
		if code, ok := IsConfigurationError(err); !ok || code != CodeConfigurationMissing {
			t.Fatalf("missing Ollama config code=%q err=%v", code, err)
		}
	}
}

type failingProvider struct{}

func (failingProvider) Name() string { return "ollama" }
func (failingProvider) Embed(context.Context, string, []string) ([][]float64, error) {
	return nil, ErrUpstream
}

func equalVectors(left, right [][]float64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if len(left[index]) != len(right[index]) {
			return false
		}
		for dimension := range left[index] {
			if left[index][dimension] != right[index][dimension] {
				return false
			}
		}
	}
	return true
}
