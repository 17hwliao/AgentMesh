package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/provider"
	"agentmesh/internal/router"
	"agentmesh/internal/runtime"
)

func TestArkFallsBackToOllamaBeforeFirstChunk(t *testing.T) {
	arkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/chat/completions" {
			http.Error(w, "first attempt unavailable", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer arkServer.Close()
	ollamaCalls := 0
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/chat" {
			ollamaCalls++
			_, _ = fmt.Fprint(w, "{\"message\":{\"content\":\"ollama fallback\"},\"done\":true}\n")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ollamaServer.Close()

	server := configuredGateway(t, arkServer.URL, ollamaServer.URL)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, chatRequestForTest())
	if body := recorder.Body.String(); !strings.Contains(body, "ollama fallback") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("fallback response=%s", body)
	}
	if ollamaCalls != 1 {
		t.Fatalf("Ollama calls=%d", ollamaCalls)
	}
}

func TestRealProviderRouteDoesNotFallbackAfterFirstChunk(t *testing.T) {
	arkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/chat/completions" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ark partial\"}}]}\n\n")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer arkServer.Close()
	ollamaCalls := 0
	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/chat" {
			ollamaCalls++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ollamaServer.Close()

	server := configuredGateway(t, arkServer.URL, ollamaServer.URL)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, chatRequestForTest())
	if body := recorder.Body.String(); !strings.Contains(body, "ark partial") || !strings.Contains(body, "stream_interrupted") {
		t.Fatalf("partial response=%s", body)
	}
	if ollamaCalls != 0 {
		t.Fatalf("Ollama calls=%d after first Ark chunk", ollamaCalls)
	}
}

func TestProviderHealthReturnsOnlyNamesAndStatus(t *testing.T) {
	providers := []provider.Provider{provider.NewMock(provider.MockConfig{Name: "safe-name", FailAfterChunks: -1})}
	recorder := httptest.NewRecorder()
	NewWithHealth(router.New(providers...), providers...).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/providers", nil))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "safe-name") || !strings.Contains(body, "healthy") || strings.Contains(body, "BaseURL") || strings.Contains(body, "APIKey") {
		t.Fatalf("health status=%d body=%s", recorder.Code, body)
	}
}

func configuredGateway(t *testing.T, arkURL, ollamaURL string) *Server {
	t.Helper()
	values := map[string]string{
		"ARK_BASE_URL":    arkURL,
		"ARK_MODEL":       "ark-test-model",
		"ARK_API_KEY":     "test-token",
		"OLLAMA_BASE_URL": ollamaURL,
		"OLLAMA_MODEL":    "ollama-test-model",
	}
	providers, err := runtime.Build("ark,ollama", func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	return NewWithHealth(router.New(providers...), providers...)
}
