package ark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmesh/internal/provider"
)

// Qwen sends empty role/finish events, reasoning deltas and a separate final
// usage event. Only answer text and complete observed usage may escape.
func TestQwenStreamContract(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/compatible-mode/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("incorrect workspace request")
				}
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid payload")
				}
				limit, absent := "max_tokens", "max_completion_tokens"
				if modern {
					limit, absent = absent, limit
				}
				if string(body[limit]) != "32" || body[absent] != nil || string(body["enable_thinking"]) != "false" || string(body["stream_options"]) != `{"include_usage":true}` {
					t.Error("incorrect generation controls")
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}],\"usage\":null}\n\n")
				fmt.Fprint(w, "data:{\"choices\":[{\"delta\":{\"reasoning_content\":\"private reasoning\"}}],\"error\":null}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7,\"completion_tokens_details\":{\"reasoning_tokens\":5},\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\ndata:[DONE]\n\n")
			}))
			defer s.Close()
			a, err := New(Config{BaseURL: s.URL + "/compatible-mode/v1", APIKey: "fixture", Name: "compatible", Models: map[string]string{"sql-review": "qwen-plus"}, DisableThinking: true, UseMaxCompletionTokens: modern}, s.Client())
			if err != nil {
				t.Fatal(err)
			}
			var chunks []provider.Chunk
			err = a.Stream(context.Background(), provider.ChatRequest{Model: "sql-review", MaxTokens: 32}, func(c provider.Chunk) error { chunks = append(chunks, c); return nil })
			if err != nil || len(chunks) != 2 || chunks[0].Delta != "OK" || chunks[1].Usage == nil || *chunks[1].Usage != (provider.Usage{InputTokens: 12, OutputTokens: 7, CachedInputTokens: 4}) {
				t.Fatalf("chunks=%+v err=%v", chunks, err)
			}
		})
	}
}

func TestQwenRejectsIncompleteOrRepeatedUsage(t *testing.T) {
	for _, frames := range []string{
		`{"usage":{}}`,
		`{"usage":{"prompt_tokens":5}}`,
		`{"usage":{"prompt_tokens":5,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":6}}}`,
		"{\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\ndata: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}",
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", frames) }))
		err := testAdapter(t, s.URL).Stream(context.Background(), testRequest(), func(provider.Chunk) error { return nil })
		s.Close()
		if !errors.Is(err, provider.ErrProtocol) {
			t.Fatalf("invalid usage accepted: %v", err)
		}
	}
}
