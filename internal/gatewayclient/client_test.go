package gatewayclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmesh/internal/provider"
)

func TestStreamWritesSSETextDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != chatPath {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") == "" {
			t.Fatal("missing Authorization header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"mock \"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"response\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	var output bytes.Buffer
	err := Stream(context.Background(), server.URL, randomAPIKey(t), "mock", []provider.Message{{Role: "user", Content: "hello"}}, &output)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if output.String() != "mock response" {
		t.Fatalf("output = %q", output.String())
	}
}

func randomAPIKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func TestStreamRejectsMissingAPIKeyBeforeRequest(t *testing.T) {
	var output bytes.Buffer
	if err := Stream(context.Background(), "http://127.0.0.1:18080", "", "mock", nil, &output); err == nil || err.Error() != "api_key_missing" {
		t.Fatalf("Stream() error=%v", err)
	}
}

func TestValidateEndpointRejectsNonLoopback(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:18080", "http://localhost:18080", "http://10.0.0.2:18080", "http://user@127.0.0.1:18080"} {
		if err := ValidateEndpoint(endpoint); err == nil {
			t.Fatalf("endpoint %q was accepted", endpoint)
		}
	}
}
