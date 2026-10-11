package billing

import (
	"agentmesh/internal/provider"
	"encoding/json"
	"testing"
	"time"
)

func TestSeparateTokenPricesIntegerRounding(t *testing.T) {
	cached := uint64(160000000)
	p := Price{InputNanoPerMillion: 800000000, OutputNanoPerMillion: 2000000000, CachedInputNanoPerMillion: &cached, MaxInput: 65536}
	charge, e := p.Cost(provider.Usage{InputTokens: 1000, OutputTokens: 100, CachedInputTokens: 200})
	if e != nil || charge != 872000 {
		t.Fatalf("charge=%d e=%v", charge, e)
	}
	p.InputNanoPerMillion = 1
	p.OutputNanoPerMillion = 1
	p.CachedInputNanoPerMillion = nil
	if c, e := p.Cost(provider.Usage{InputTokens: 1, OutputTokens: 1}); e != nil || c != 1 {
		t.Fatal(c, e)
	}
	for _, u := range []provider.Usage{{InputTokens: -1}, {InputTokens: 10, CachedInputTokens: 11}, {InputTokens: 65537}, {OutputTokens: 4097}, {Estimated: true}} {
		if _, e = p.Cost(u); e == nil {
			t.Fatal("bad usage accepted", u)
		}
	}
}
func TestPriceVersionAndExpiry(t *testing.T) {
	c := Catalog{Currency: "CNY", ExpiresAt: time.Now().Add(time.Hour), Prices: []Price{{Provider: "compatible", Model: "sql-review", UpstreamModel: "qwen-plus", MaxInput: 32768, InputNanoPerMillion: 800000000, OutputNanoPerMillion: 2000000000, Source: "official catalog fixture"}}}
	raw, _ := json.Marshal(c)
	_, a, e := Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	c.Prices[0].OutputNanoPerMillion++
	raw, _ = json.Marshal(c)
	_, b, e := Decode(raw)
	if e != nil || a == b {
		t.Fatal("price change did not change version")
	}
	c.ExpiresAt = time.Now().Add(-time.Second)
	raw, _ = json.Marshal(c)
	if _, _, e = Decode(raw); e == nil {
		t.Fatal("expired tariff accepted")
	}
}
