package semanticcache

import (
	"agentmesh/internal/provider"
	"context"
	"testing"
)

func TestImageHistoryBypassesTextSemanticCache(t *testing.T) {
	// Even if the current user turn is plain text, earlier image context makes
	// the full conversation ineligible before any Redis/embedding work.
	r := provider.ChatRequest{Messages: []provider.Message{{Role: "user", Parts: []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: "https://example.com/a.png"}}}}, {Role: "assistant", Content: "prior"}, {Role: "user", Content: "same question"}}}
	if _, e := (&Cache{}).Prepare(context.Background(), "t", "v", "route", r); e == nil || e.Error() != "cache_ineligible" {
		t.Fatal("image context reached text cache")
	}
}
