package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/auth"
	"agentmesh/internal/embedding"
	"agentmesh/internal/router"
)

func TestAuthenticatedHandlerMountsEmbeddingEndpoint(t *testing.T) {
	key := "embedding-gateway-key-123456"
	values := map[string]string{
		"AGENTMESH_BOOTSTRAP_API_KEY":      key,
		"AGENTMESH_BOOTSTRAP_TENANT_ID":    "tenant_a",
		"AGENTMESH_BOOTSTRAP_MODEL_ROUTES": `{"embed-model":["mock"]}`,
	}
	store, err := auth.Bootstrap(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	provider, err := embedding.NewMockProvider(8)
	if err != nil {
		t.Fatal(err)
	}
	embeddingHandler, err := embedding.NewHandler(store, []embedding.Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithHealth(router.New())
	server.SetEmbeddingHandler(embeddingHandler)
	protected := server.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })

	unauthorized := httptest.NewRecorder()
	protected.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, embedding.EmbeddingsPath, strings.NewReader(`{"model":"embed-model","input":"x"}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized embedding status=%d", unauthorized.Code)
	}
	authorized := httptest.NewRequest(http.MethodPost, embedding.EmbeddingsPath, strings.NewReader(`{"model":"embed-model","input":"x"}`))
	authorized.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, authorized)
	if recorder.Code != http.StatusOK || recorder.Header().Get("X-AgentMesh-Trace-ID") == "" {
		t.Fatalf("authorized embedding status=%d trace=%q body=%s", recorder.Code, recorder.Header().Get("X-AgentMesh-Trace-ID"), recorder.Body.String())
	}
}
