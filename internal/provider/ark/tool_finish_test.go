package ark

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/provider"
	"agentmesh/internal/router"
)

func toolFinishRequest() provider.ChatRequest {
	return provider.ChatRequest{Model: "m", MaxTokens: 64, Messages: []provider.Message{{Role: "user", Content: "fixture"}}, Tools: []provider.ToolDefinition{{Type: "function", Function: provider.ToolFunction{Name: "read", Parameters: []byte(`{"type":"object"}`)}}}}
}

func toolFinishAdapter(t *testing.T, body string) *Adapter {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	t.Cleanup(upstream.Close)
	adapter, err := New(Config{BaseURL: upstream.URL, APIKey: "fixture-key-not-real", Name: "compatible", Models: map[string]string{"m": "fixture"}, ToolModels: []string{"m"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestToolFinishRequiresActualCompleteCall(t *testing.T) {
	const first = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}` + "\n\n"
	const rest = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"fixture\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
	const finish = `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
	const stop = `data: {"choices":[{"delta":{"content":"plain answer"},"finish_reason":"stop"}]}` + "\n\n"
	const usage = `data: {"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":1}}` + "\n\n"
	const done = "data: [DONE]\n\n"
	for _, test := range []struct {
		name, body string
		ok         bool
		calls      int
	}{
		{"empty_tool_finish", finish + usage + done, false, 0},
		{"fragmented_complete_tool", first + rest + usage + done, true, 2},
		{"auto_without_tool_is_normal_stop", stop + usage + done, true, 0},
		{"missing_tool_finish_at_done", first + strings.Replace(rest, `,"finish_reason":"tool_calls"`, "", 1) + usage + done, false, 2},
		{"incomplete_arguments_at_finish", first + finish + usage + done, false, 1},
		{"usage_then_empty_tool_finish", usage + finish + done, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := toolFinishAdapter(t, test.body)
			calls := 0
			err := adapter.Stream(context.Background(), toolFinishRequest(), func(chunk provider.Chunk) error { calls += len(chunk.ToolCalls); return nil })
			if (err == nil) != test.ok || !test.ok && !errors.Is(err, provider.ErrProtocol) || calls != test.calls {
				t.Fatalf("ok=%t err=%v call_deltas=%d want=%d", test.ok, err, calls, test.calls)
			}
		})
	}
}

type toolFinishHook struct {
	started, receipts int
	outcome           string
}

func (h *toolFinishHook) BeforeAttempt(context.Context, string, provider.ChatRequest) error {
	h.started++
	return nil
}
func (h *toolFinishHook) BeforeEmit(_ context.Context, _ string, chunk provider.Chunk) error {
	if chunk.Usage != nil {
		h.receipts++
	}
	return nil
}
func (*toolFinishHook) AfterEmit(context.Context, string, provider.Chunk) error { return nil }
func (h *toolFinishHook) FinishAttempt(_ context.Context, _, outcome string) error {
	h.outcome = outcome
	return nil
}

func TestToolCutoffDoesNotInventUsageOrFallbackAfterArguments(t *testing.T) {
	adapter := toolFinishAdapter(t, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}`+"\n\n")
	fallback := toolFinishAdapter(t, `data: {"choices":[{"delta":{"content":"must not run"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	hook := &toolFinishHook{}
	deltas := 0
	err := router.New(adapter, fallback).StreamWithAttemptHook(context.Background(), toolFinishRequest(), func(chunk provider.Chunk) error { deltas += len(chunk.ToolCalls); return nil }, hook)
	if !errors.Is(err, router.ErrStreamInterrupted) || deltas != 1 || hook.started != 1 || hook.receipts != 0 || hook.outcome != "interrupted" {
		t.Fatalf("tool cutoff was made successful/free or retried: err=%v deltas=%d hook=%+v", err, deltas, hook)
	}
	// This asserts the billing hook inputs only. The real money Store keeps a
	// started attempt with no receipt pending reconciliation; the test does not
	// substitute a fake hook for a real MySQL billing proof.
}
