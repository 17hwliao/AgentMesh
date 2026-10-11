package semanticcache

import (
	"agentmesh/internal/embedding"
	"agentmesh/internal/provider"
	"context"
	"encoding/json"
	"fmt"
	redis "github.com/redis/go-redis/v9"
	"os"
	"testing"
	"time"
)

func TestRealRedisCacheIsolationTTLAndBound(t *testing.T) {
	raw := os.Getenv("AGENTMESH_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("acceptance-only Redis required")
	}
	opt, e := redis.ParseURL(raw)
	if e != nil {
		t.Fatal("invalid Redis config")
	}
	r := redis.NewClient(opt)
	defer r.Close()
	embed, _ := embedding.NewMockProvider(32)
	c := &Cache{Redis: r, Embedder: embed, EmbeddingModel: "fixture", Version: "v1", Threshold: .995, TTL: time.Minute}
	zero := 0.
	ctx := context.Background()
	tenant := fmt.Sprintf("cache-%d", time.Now().UnixNano())
	req := provider.ChatRequest{Model: "m", Temperature: &zero, MaxTokens: 32, Messages: []provider.Message{{Role: "user", Content: "show order 42"}}}
	p, e := c.Prepare(ctx, tenant, "docs-v1", "compatible", req)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Put(ctx, p, "answer"); e != nil {
		t.Fatal(e)
	}
	if text, hit, e := c.Get(ctx, p); e != nil || !hit || text != "answer" {
		t.Fatal("cache miss", e)
	}
	for _, variant := range []Prepared{{Tenant: tenant + "x", Namespace: p.Namespace, Prompt: p.Prompt, Vector: p.Vector}, {Tenant: tenant, Namespace: "other-context", Prompt: p.Prompt, Vector: p.Vector}, {Tenant: tenant, Namespace: p.Namespace, Prompt: "show order 43", Vector: p.Vector}} {
		if _, hit, _ := c.Get(ctx, variant); hit {
			t.Fatal("scope or numeric boundary leaked")
		}
	}
	data, order := keys(tenant)
	ids, _ := r.ZRange(ctx, order, 0, -1).Result()
	rawEntry, _ := r.HGet(ctx, data, ids[0]).Result()
	var entry Entry
	if json.Unmarshal([]byte(rawEntry), &entry) != nil {
		t.Fatal("bad entry")
	}
	entry.Expires = time.Now().Add(-time.Second)
	bytes, _ := json.Marshal(entry)
	r.HSet(ctx, data, ids[0], bytes)
	if _, hit, _ := c.Get(ctx, p); hit {
		t.Fatal("expired entry returned")
	}
	for i := 0; i < 140; i++ {
		p.Prompt = fmt.Sprint("order ", i)
		if e = c.Put(ctx, p, "ok"); e != nil {
			t.Fatal(e)
		}
	}
	if n, _ := r.HLen(ctx, data).Result(); n != 128 {
		t.Fatal("cache capacity not bounded", n)
	}
	if n, _ := r.ZCard(ctx, order).Result(); n != 128 {
		t.Fatal("cache order not bounded", n)
	}
}
