package ark

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

func TestStreamMapsOpenAIStyleSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" || request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected Ark request path=%s authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ark \"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"output\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := testAdapter(t, server.URL)
	var output strings.Builder
	err := adapter.Stream(context.Background(), testRequest(), func(chunk provider.Chunk) error { _, err := output.WriteString(chunk.Delta); return err })
	if err != nil || output.String() != "ark output" {
		t.Fatalf("Stream() error=%v output=%q", err, output.String())
	}
}

func TestStreamHidesUpstreamErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Error(w, "sensitive upstream body", http.StatusUnauthorized)
	}))
	defer server.Close()
	err := testAdapter(t, server.URL).Stream(context.Background(), testRequest(), func(provider.Chunk) error { return nil })
	if !errors.Is(err, provider.ErrUpstream) || strings.Contains(fmt.Sprint(err), "sensitive") {
		t.Fatalf("Stream() error=%v", err)
	}
}

func TestStreamCancellationReachesUpstream(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	adapter, err := New(Config{BaseURL: "http://127.0.0.1:18080", Model: "ark-test-model", APIKey: "test-token"}, &http.Client{Transport: waitingTransport{started: started, cancelled: cancelled}})
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
		t.Fatal("Ark request did not start")
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

func TestHealthUsesModelsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" {
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
	adapter, err := New(Config{BaseURL: baseURL, Model: "ark-test-model", APIKey: "test-token"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func testRequest() provider.ChatRequest {
	return provider.ChatRequest{Model: "ignored-by-adapter", Messages: []provider.Message{{Role: "user", Content: "hello"}}}
}
