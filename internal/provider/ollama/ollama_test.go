package ollama

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentmesh/internal/provider"
)

func TestStreamMapsNativeNDJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/chat" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprint(w, "{\"message\":{\"content\":\"ollama \"},\"done\":false}\n{\"message\":{\"content\":\"output\"},\"done\":true}\n")
	}))
	defer server.Close()
	var output strings.Builder
	err := testAdapter(t, server.URL).Stream(context.Background(), testRequest(), func(chunk provider.Chunk) error { _, err := output.WriteString(chunk.Delta); return err })
	if err != nil || output.String() != "ollama output" {
		t.Fatalf("Stream() error=%v output=%q", err, output.String())
	}
}

func TestStreamRejectsUpstreamErrorWithoutEcho(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(w, "{\"error\":\"sensitive upstream detail\"}\n")
	}))
	defer server.Close()
	err := testAdapter(t, server.URL).Stream(context.Background(), testRequest(), func(provider.Chunk) error { return nil })
	if !errors.Is(err, provider.ErrUpstream) || strings.Contains(fmt.Sprint(err), "sensitive") {
		t.Fatalf("Stream() error=%v", err)
	}
}

func TestStreamCancellationReachesUpstream(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	adapter, err := New(Config{BaseURL: "http://127.0.0.1:18080", Model: "ollama-test-model"}, &http.Client{Transport: waitingTransport{started: started, cancelled: cancelled}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- adapter.Stream(ctx, testRequest(), func(provider.Chunk) error { return nil })
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Ollama request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream() error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive cancellation")
	}
}

type waitingTransport struct {
	started   chan<- struct{}
	cancelled chan<- struct{}
}

func (t waitingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	close(t.started)
	<-request.Context().Done()
	close(t.cancelled)
	return nil, request.Context().Err()
}

func TestHealthUsesTagsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/tags" {
			t.Fatalf("health path=%s", request.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := testAdapter(t, server.URL).Health(context.Background()); err != nil {
		t.Fatalf("Health() error=%v", err)
	}
}

func testAdapter(t *testing.T, baseURL string) *Adapter {
	t.Helper()
	adapter, err := New(Config{BaseURL: baseURL, Model: "ollama-test-model"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func testRequest() provider.ChatRequest {
	return provider.ChatRequest{Model: "ignored-by-adapter", Messages: []provider.Message{{Role: "user", Content: "hello"}}}
}
