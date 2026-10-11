package modelrpc

import (
	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/securegrpc"
	"agentmesh/internal/testcert"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixtureProvider struct{ cancelled chan struct{} }

func (fixtureProvider) Name() string                 { return "compatible" }
func (fixtureProvider) BillingModel(string) string   { return "qwen-plus" }
func (fixtureProvider) Health(context.Context) error { return nil }
func (p fixtureProvider) Stream(ctx context.Context, r provider.ChatRequest, emit provider.Emit) error {
	if e := emit(provider.Chunk{Delta: "hello"}); e != nil {
		return e
	}
	if r.Messages[0].Content == "cancel" {
		<-ctx.Done()
		p.cancelled <- struct{}{}
		return ctx.Err()
	}
	return emit(provider.Chunk{Usage: &provider.Usage{InputTokens: 10, OutputTokens: 2}})
}
func TestMTLSModelStreamTenantGateAndCancellation(t *testing.T) {
	ca, sc, sk, cc, ck, e := testcert.Generate(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("m", 32)
	server, e := securegrpc.Server(securegrpc.Config{CA: ca, Cert: sc, Key: sk, Token: token})
	if e != nil {
		t.Fatal(e)
	}
	defer server.Stop()
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	cancelled := make(chan struct{}, 1)
	RegisterModelRunnerServer(server, NewServer([]provider.Provider{fixtureProvider{cancelled}}, func(_ context.Context, tenant, model, name string) bool {
		return tenant == "tenant-a" && model == "m" && name == "compatible"
	}))
	go server.Serve(listener)
	conn, e := securegrpc.Dial(listener.Addr().String(), securegrpc.Config{CA: ca, Cert: cc, Key: ck, Token: token, ServerName: "localhost"})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	adapter := NewAdapter(conn, "compatible", map[string]string{"m": "qwen-plus"}, "")
	if adapter.ValidateBilling(context.Background(), "m", "qwen-plus") != nil {
		t.Fatal("valid remote price binding rejected")
	}
	if adapter.ValidateBilling(context.Background(), "m", "wrong-price-model") == nil {
		t.Fatal("remote tariff drift accepted")
	}
	store, e := auth.Bootstrap(func(name string) string {
		switch name {
		case "AGENTMESH_BOOTSTRAP_API_KEY":
			return "fixture-key-for-model-rpc"
		case "AGENTMESH_BOOTSTRAP_TENANT_ID":
			return "tenant-a"
		case "AGENTMESH_BOOTSTRAP_MODEL_ROUTES":
			return `{"m":["compatible"]}`
		}
		return ""
	})
	if e != nil {
		t.Fatal(e)
	}
	invoke := func(content, model string, ctx context.Context) error {
		var result error
		handler := auth.Authenticate(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result = adapter.Stream(r.Context(), provider.ChatRequest{Model: model, MaxTokens: 32, Messages: []provider.Message{{Role: "user", Content: content}}}, func(c provider.Chunk) error { return nil })
		}))
		req := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer fixture-key-for-model-rpc")
		handler.ServeHTTP(httptest.NewRecorder(), req)
		return result
	}
	if e = invoke("hello", "m", context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = invoke("hello", "other", context.Background()); e == nil {
		t.Fatal("unauthorized model reached provider")
	}
	if e = adapter.Stream(context.Background(), provider.ChatRequest{}, func(provider.Chunk) error { return nil }); !errors.Is(e, provider.ErrProtocol) {
		t.Fatal("tenant context was optional")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if e = invoke("cancel", "m", ctx); e == nil {
		t.Fatal("cancelled stream succeeded")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach provider")
	}
}
