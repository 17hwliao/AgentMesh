package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"agentmesh/internal/auth"
	"agentmesh/internal/embedding"
)

func main() {
	const key = "embedding-demo-key-123456"
	values := map[string]string{
		"AGENTMESH_BOOTSTRAP_API_KEY":      key,
		"AGENTMESH_BOOTSTRAP_TENANT_ID":    "tenant_demo",
		"AGENTMESH_BOOTSTRAP_MODEL_ROUTES": `{"embed-model":["mock"]}`,
	}
	store, err := auth.Bootstrap(func(name string) string { return values[name] })
	if err != nil {
		fail(err)
	}
	provider, err := embedding.NewMockProvider(8)
	if err != nil {
		fail(err)
	}
	handler, err := embedding.NewHandler(store, []embedding.Provider{provider})
	if err != nil {
		fail(err)
	}
	server := httptest.NewServer(auth.Authenticate(store, handler))
	defer server.Close()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+embedding.EmbeddingsPath, bytes.NewBufferString(`{"model":"embed-model","input":["offline demo"]}`))
	if err != nil {
		fail(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fail(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		fail(err)
	}
	fmt.Printf("status=%d trace=%s\n%s", response.StatusCode, response.Header.Get("X-AgentMesh-Trace-ID"), body)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "embedding-demo:", err)
	os.Exit(1)
}
