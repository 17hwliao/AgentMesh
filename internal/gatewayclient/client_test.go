package gatewayclient

import (
	"bytes"
	"context"
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
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"mock \"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"response\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	var output bytes.Buffer
	err := Stream(context.Background(), server.URL, "mock", []provider.Message{{Role: "user", Content: "hello"}}, &output)
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if output.String() != "mock response" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestValidateEndpointRejectsNonLoopback(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:18080", "http://localhost:18080", "http://10.0.0.2:18080", "http://user@127.0.0.1:18080"} {
		if err := ValidateEndpoint(endpoint); err == nil {
			t.Fatalf("endpoint %q was accepted", endpoint)
		}
	}
}
