package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentmesh/internal/provider"
	"agentmesh/internal/tenant"
)

func toolChunk(args string, first bool) provider.Chunk {
	d := provider.ToolCallDelta{Index: 0, Function: provider.CallFunction{Arguments: args}}
	if first {
		d.ID = "call_1"
		d.Type = "function"
		d.Function.Name = "Read"
	}
	return provider.Chunk{ToolCalls: []provider.ToolCallDelta{d}}
}
func completionFixture(t *testing.T, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c := newCompletion(w, "domestic-chat", stream)
	for _, p := range []provider.Chunk{toolChunk(`{"path":`, true), toolChunk(`"x"}`, false), {FinishReason: "tool_calls", Usage: &provider.Usage{InputTokens: 123, OutputTokens: 17, CachedInputTokens: 20}}} {
		if e := c.emit(p); e != nil {
			t.Fatal(e)
		}
	}
	if e := c.complete(); e != nil {
		t.Fatal(e)
	}
	return w
}
func TestChatCompletionsHaveCanonicalStreamAndNonstreamShape(t *testing.T) {
	w := completionFixture(t, false)
	var row map[string]any
	if json.Unmarshal(w.Body.Bytes(), &row) != nil || row["object"] != "chat.completion" || row["model"] != "domestic-chat" || row["created"].(float64) <= 0 {
		t.Fatal(w.Body.String())
	}
	choice := row["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if choice["finish_reason"] != "tool_calls" || msg["content"] != nil || msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"] != `{"path":"x"}` {
		t.Fatal(row)
	}
	if row["usage"].(map[string]any)["total_tokens"] != float64(140) {
		t.Fatal(row)
	}
	stream := completionFixture(t, true).Body.String()
	id := ""
	frames := 0
	for _, frame := range strings.Split(stream, "\n\n") {
		if !strings.HasPrefix(frame, "data: {") {
			continue
		}
		var row map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(frame, "data: ")), &row) != nil {
			t.Fatal(frame)
		}
		if id == "" {
			id = row["id"].(string)
		}
		if row["id"] != id || row["object"] != "chat.completion.chunk" {
			t.Fatal(frame)
		}
		frames++
	}
	if frames != 5 || !strings.HasSuffix(stream, "data: [DONE]\n\n") {
		t.Fatal(stream)
	}
}
func TestAnthropicBridgeStreamToolFragmentsAndActualUsage(t *testing.T) {
	source := completionFixture(t, true).Body.String()
	w := httptest.NewRecorder()
	b := &anthropicBridge{w: w, stream: true, model: "domestic-chat", blocks: map[int]int{}, open: map[int]bool{}}
	// Exercise arbitrary HTTP write boundaries, not just one frame per Write.
	for i := 0; i < len(source); i += 7 {
		end := i + 7
		if end > len(source) {
			end = len(source)
		}
		if _, e := b.Write([]byte(source[i:end])); e != nil {
			t.Fatal(e)
		}
	}
	out := w.Body.String()
	for _, want := range []string{"event: message_start", "event: content_block_start", `"type":"tool_use"`, `"type":"input_json_delta"`, `"stop_reason":"tool_use"`, `"input_tokens":103`, `"cache_read_input_tokens":20`, `"output_tokens":17`, "event: message_stop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
	converted, e := anthropicCompletion(func() map[string]any {
		var r map[string]any
		_ = json.Unmarshal(completionFixture(t, false).Body.Bytes(), &r)
		return r
	}())
	if e != nil || converted["stop_reason"] != "tool_use" {
		t.Fatal(converted, e)
	}
}
func TestAnthropicMissingUsageCannotReportSuccessfulZeroCost(t *testing.T) {
	w := httptest.NewRecorder()
	b := &anthropicBridge{w: w, stream: true, blocks: map[int]int{}, open: map[int]bool{}}
	_, e := b.Write([]byte("data: {\"id\":\"x\",\"choices\":[]}\n\ndata: [DONE]\n\n"))
	if e != nil || !strings.Contains(w.Body.String(), "usage_unavailable") || strings.Contains(w.Body.String(), "event: message_stop") {
		t.Fatal(w.Body.String(), e)
	}
}
func TestAnthropicConversionPreservesToolHistoryAndRejectsUnsupportedFeatures(t *testing.T) {
	raw := `{"model":"domestic-chat","max_tokens":512,"system":[{"type":"text","text":"system"}],"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"read"},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{"path":"x"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"random result"}]}]}]}`
	var a anthropicRequest
	if json.Unmarshal([]byte(raw), &a) != nil {
		t.Fatal("fixture")
	}
	r, e := convertAnthropic(a)
	if e != nil || len(r.Messages) != 4 || r.Messages[3].Role != "tool" || r.Messages[3].ToolCallID != "call_1" || r.Messages[3].Content != "random result" {
		t.Fatal(r, e)
	}
	a.Thinking = json.RawMessage(`{"type":"adaptive"}`)
	if _, e = convertAnthropic(a); e == nil {
		t.Fatal("unsupported thinking accepted")
	}
	a.Thinking = nil
	a.OutputConfig = json.RawMessage(`{"format":{"type":"json_schema"}}`)
	if _, e = convertAnthropic(a); e == nil {
		t.Fatal("unsupported schema accepted")
	}
}

type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *deadlineWriter) SetWriteDeadline(d time.Time) error { w.deadline = d; return nil }
func TestAnthropicWrappersPreserveDeadlinesAndAuthErrorTypes(t *testing.T) {
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	b := &anthropicBridge{w: &anthropicErrorWriter{ResponseWriter: w}}
	want := time.Now().Add(time.Second)
	if http.NewResponseController(b).SetWriteDeadline(want) != nil || !w.deadline.Equal(want) {
		t.Fatal("deadline blocked by protocol bridge")
	}
	raw, store, mock := authenticatedStore(t)
	handler := newAuthenticatedServer(t, store, mock, nil)
	for _, key := range []string{"", raw} {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"bad":true}`))
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		want := 401
		if key != "" {
			want = 400
		}
		if rec.Code != want || !strings.Contains(rec.Body.String(), `"type":"error"`) || mock.Calls() != 0 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("x-api-key", raw)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 401 || !strings.Contains(rec.Body.String(), "conflicting_credentials") {
		t.Fatal(rec.Body.String())
	}
}
func TestModelDiscoveryIsAuthenticatedAndTenantScoped(t *testing.T) {
	a, b := randomRawKey(t), randomRawKey(t)
	store := tenant.NewMemory([]tenant.Tenant{{ID: "a", Enabled: true, ModelRoutes: map[string][]string{"visible-a": {"mock"}}}, {ID: "b", Enabled: true, ModelRoutes: map[string][]string{"visible-b": {"mock"}}}}, []tenant.APIKeyRecord{record(a, "a"), record(b, "b")})
	mock := provider.NewMock(provider.MockConfig{Name: "mock-visible", Chunks: []string{"ok"}, FailAfterChunks: -1})
	handler := newAuthenticatedServer(t, store, mock, nil)
	for _, test := range []struct {
		path, key string
		status    int
		contains  string
	}{{"/v1/models", "", 401, "auth_failed"}, {"/v1/models", a, 200, "visible-a"}, {"/v1/models/visible-a", a, 200, "visible-a"}, {"/v1/models/visible-b", a, 404, "model_not_found"}} {
		req := httptest.NewRequest("GET", test.path, nil)
		req.Header.Set("Authorization", "Bearer "+test.key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != test.status || !strings.Contains(w.Body.String(), test.contains) || (test.status == 200 && strings.Contains(w.Body.String(), "visible-b")) {
			t.Fatal(fmt.Sprint(w.Code), w.Body.String())
		}
	}
	if mock.Calls() != 0 {
		t.Fatal("discovery invoked a model")
	}
}
