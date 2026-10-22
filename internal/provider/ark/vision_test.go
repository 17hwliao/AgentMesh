package ark

import (
	"agentmesh/internal/provider"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestVisionPreservesMixedHistoryAndBlocksTextOnlyAlias(t *testing.T) {
	calls := 0
	msgs := []provider.Message{{Role: "system", Content: "answer in Chinese"}, {Role: "user", Parts: []provider.ContentPart{{Type: "text", Text: "what is in this image?"}, {Type: "image_url", ImageURL: &provider.ImageURL{URL: "https://example.com/photo.png"}}}}, {Role: "assistant", Content: "earlier answer"}, {Role: "user", Content: "what color?"}}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Model    string             `json:"model"`
			Messages []provider.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "actual-vision" || !reflect.DeepEqual(body.Messages, msgs) {
			t.Error("context/image was changed")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"red\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":300,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	a, e := New(Config{BaseURL: s.URL, APIKey: "fixture", Name: "compatible", Models: map[string]string{"vision": "actual-vision", "text": "actual-text"}, VisionModels: []string{"vision"}}, s.Client())
	if e != nil {
		t.Fatal(e)
	}
	r := provider.ChatRequest{Model: "text", Messages: msgs, MaxTokens: 32}
	if a.Stream(context.Background(), r, func(provider.Chunk) error { return nil }) != provider.ErrUnsupportedInput || calls != 0 {
		t.Fatal("text-only adapter sent images")
	}
	r.Model = "vision"
	var chunks []provider.Chunk
	if a.Stream(context.Background(), r, func(c provider.Chunk) error { chunks = append(chunks, c); return nil }) != nil || calls != 1 || len(chunks) != 2 || chunks[1].Usage.InputTokens != 300 {
		t.Fatal("vision stream/usage failed")
	}
}
