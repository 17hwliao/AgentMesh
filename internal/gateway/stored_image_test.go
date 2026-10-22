package gateway

import (
	"agentmesh/internal/provider"
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStoredImageResolutionPrecedesValidationAndOnlyResolvedDataReachesAdapter(t *testing.T) {
	// A valid one-pixel PNG fixture from the existing image validator suite.
	resolved := 0
	resolve := func(ms []provider.Message) error {
		resolved++
		if ms[0].Parts[0].ImageURL.URL != "agentmesh://images/img_fixture" {
			t.Fatal("reference lost")
		}
		ms[0].Parts[0].ImageURL.URL = "https://example.com/resolved.png"
		return nil
	}
	request := `{"model":"vision","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"agentmesh://images/img_fixture"}}]}]}`
	input, e := decodeChatRequest(httptest.NewRequest("POST", chatPath, strings.NewReader(request)), resolve)
	if e != nil || resolved != 1 || input.Messages[0].Parts[0].ImageURL.URL != "https://example.com/resolved.png" {
		t.Fatal("reference rejected before resolution", e)
	}
	if _, e = decodeChatRequest(httptest.NewRequest("POST", chatPath, bytes.NewBufferString(request))); e == nil {
		t.Fatal("unresolved private URI accepted")
	}
	var a anthropicRequest
	_ = json.Unmarshal([]byte(`{"model":"vision","max_tokens":64,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"agentmesh://images/img_fixture"}}]}]}`), &a)
	if _, e = convertAnthropic(a, resolve); e != nil || resolved != 2 {
		t.Fatal("Messages private image not resolved", e)
	}
}
