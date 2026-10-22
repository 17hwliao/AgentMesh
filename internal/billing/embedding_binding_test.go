package billing

import (
	"agentmesh/internal/provider"
	"testing"
)

func TestEmbeddingPriceBindingDoesNotBroadenChatProviders(t *testing.T) {
	s := &Store{Catalog: Catalog{Prices: []Price{{Provider: "compatible", Model: "embedding", UpstreamModel: "cloud-embedding", InputNanoPerMillion: 125000000, MaxInput: 65536}}}}
	if s.VerifyProviders(nil) == nil {
		t.Fatal("unbound embedding price accepted")
	}
	b := ModelBinding{"compatible", "embedding", "cloud-embedding"}
	if s.VerifyProviders(nil, b) != nil {
		t.Fatal("explicit binding refused")
	}
	b.UpstreamModel = "other"
	if s.VerifyProviders(nil, b) == nil {
		t.Fatal("price bound to wrong model")
	}
	b.UpstreamModel = "cloud-embedding"
	if s.VerifyProviders(nil, b, b) == nil {
		t.Fatal("duplicate binding accepted")
	}
	charge, err := s.Catalog.Prices[0].Cost(provider.Usage{InputTokens: 48})
	if err != nil || charge != 6000 {
		t.Fatal(charge, err)
	}
}
