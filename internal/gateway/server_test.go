package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/provider"
	"agentmesh/internal/router"
)

func TestValidateListenAddressAllowsOnlyLoopback(t *testing.T) {
	if err := ValidateListenAddress("127.0.0.1:18080"); err != nil {
		t.Fatalf("loopback address rejected: %v", err)
	}
	for _, address := range []string{"0.0.0.0:18080", "192.168.1.10:18080", "localhost:18080", "127.0.0.1"} {
		if err := ValidateListenAddress(address); err == nil {
			t.Fatalf("non-loopback address %q was accepted", address)
		}
	}
}

func TestChatFallsBackBeforeFirstChunk(t *testing.T) {
	primary := provider.NewMock(provider.MockConfig{Name: "primary", FailBeforeFirst: true, FailAfterChunks: -1})
	fallback := provider.NewMock(provider.MockConfig{Name: "fallback", Chunks: []string{"usable"}, FailAfterChunks: -1})
	recorder := httptest.NewRecorder()
	New(router.New(primary, fallback)).Handler().ServeHTTP(recorder, chatRequestForTest())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "usable") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("unexpected SSE body: %s", body)
	}
	if fallback.Calls() != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallback.Calls())
	}
}

func TestChatDoesNotFallbackAfterFirstChunk(t *testing.T) {
	primary := provider.NewMock(provider.MockConfig{Name: "primary", Chunks: []string{"partial"}, FailAfterChunks: 1})
	fallback := provider.NewMock(provider.MockConfig{Name: "fallback", Chunks: []string{"must not run"}, FailAfterChunks: -1})
	recorder := httptest.NewRecorder()
	New(router.New(primary, fallback)).Handler().ServeHTTP(recorder, chatRequestForTest())

	if body := recorder.Body.String(); !strings.Contains(body, "partial") || !strings.Contains(body, "stream_interrupted") {
		t.Fatalf("unexpected SSE body: %s", body)
	}
	if fallback.Calls() != 0 {
		t.Fatalf("fallback calls = %d after partial stream", fallback.Calls())
	}
}

func TestChatRejectsUnknownJSONFields(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, chatPath, bytes.NewBufferString(`{"model":"mock","messages":[{"role":"user","content":"x"}],"stream":true,"tenant":"not accepted"}`))
	New(router.New()).Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_chat_request") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func chatRequestForTest() *http.Request {
	body := `{"model":"mock-model","messages":[{"role":"user","content":"hello"}],"stream":true}`
	return httptest.NewRequest(http.MethodPost, chatPath, bytes.NewBufferString(body))
}
