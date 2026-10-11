package ark

import (
	"agentmesh/internal/provider"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompatibleRoutingAndCachedUsage(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing credential")
		}
		var v map[string]any
		if json.NewDecoder(r.Body).Decode(&v) != nil || v["model"] != "qwen-plus" || v["enable_thinking"] != false {
			t.Error("model mapping failed")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	p, e := New(Config{BaseURL: s.URL, APIKey: "fixture", Name: "compatible", Models: map[string]string{"sql-review": "qwen-plus"}, DisableThinking: true}, s.Client())
	if e != nil {
		t.Fatal(e)
	}
	var usage *provider.Usage
	e = p.Stream(context.Background(), provider.ChatRequest{Model: "sql-review", Messages: []provider.Message{{Role: "user", Content: "hello"}}}, func(c provider.Chunk) error {
		if c.Usage != nil {
			usage = c.Usage
		}
		return nil
	})
	if e != nil || p.Name() != "compatible" || usage == nil || usage.CachedInputTokens != 4 {
		t.Fatalf("usage=%+v err=%v", usage, e)
	}
	if p.Stream(context.Background(), provider.ChatRequest{Model: "unmapped"}, func(provider.Chunk) error { return nil }) == nil {
		t.Fatal("unknown alias accepted")
	}
}

func TestCompatibleNeverFollowsRedirect(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	p, _ := New(Config{BaseURL: source.URL, APIKey: "fixture", Model: "model"}, nil)
	if p.Stream(context.Background(), provider.ChatRequest{}, func(provider.Chunk) error { return nil }) == nil || calls != 0 {
		t.Fatal("redirect leaked credentials")
	}
}
