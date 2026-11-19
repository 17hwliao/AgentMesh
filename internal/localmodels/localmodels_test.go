package localmodels

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeEndpoint(t *testing.T) {
	ollama, err := NormalizeEndpoint(KindOllama, "http://127.0.0.1:11434/v1")
	if err != nil {
		t.Fatal(err)
	}
	if ollama.BaseURL != "http://127.0.0.1:11434" || ollama.DiscoveryURL != "http://127.0.0.1:11434/api/tags" || ollama.ChatBaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("ollama endpoint = %+v", ollama)
	}
	lm, err := NormalizeEndpoint(KindLMStudio, "http://127.0.0.1:1234")
	if err != nil {
		t.Fatal(err)
	}
	if lm.BaseURL != "http://127.0.0.1:1234/v1" || lm.DiscoveryURL != "http://127.0.0.1:1234/v1/models" || lm.ChatBaseURL != "http://127.0.0.1:1234/v1" {
		t.Fatalf("lm studio endpoint = %+v", lm)
	}
}

func TestDiscoverOllamaAndLMStudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3:8b"},{"model":"qwen2.5:7b"}]}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ollama, err := Discover(context.Background(), KindOllama, server.URL, nil)
	if err != nil || !ollama.Reachable || len(ollama.Models) != 2 {
		t.Fatalf("ollama source = %+v err=%v", ollama, err)
	}
	lm, err := Discover(context.Background(), KindLMStudio, server.URL+"/v1", nil)
	if err != nil || !lm.Reachable || len(lm.Models) != 1 || lm.Models[0] != "local-model" {
		t.Fatalf("lm studio source = %+v err=%v", lm, err)
	}
}

func TestDiscoverRejectsNonLoopback(t *testing.T) {
	_, err := Discover(context.Background(), KindOllama, "http://192.0.2.10:11434", nil)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback error = %v", err)
	}
}

func TestDiscoverRejectsRedirectWithoutFollowing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer server.Close()
	_, err := Discover(context.Background(), KindOllama, server.URL, nil)
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestDiscoverRejectsOversizeAndOmitsInvalidURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"models":[]}`+strings.Repeat(" ", maxDiscoveryBytes))
	}))
	defer server.Close()
	if _, err := Discover(context.Background(), KindOllama, server.URL, nil); err == nil || err.Error() != "local_models_unreadable" {
		t.Fatalf("oversize error = %v", err)
	}
	source, err := Discover(context.Background(), KindOllama, server.URL+"?token=private-fixture", nil)
	if err == nil || source.BaseURL != "" {
		t.Fatalf("invalid URL was reflected: %+v, err=%v", source, err)
	}
	if _, err := NormalizeEndpoint(KindOllama, "https://127.0.0.1:11434"); err == nil {
		t.Fatal("HTTPS must be rejected by the local HTTP discovery contract")
	}
}
