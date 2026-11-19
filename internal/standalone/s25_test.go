package standalone_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentmesh/internal/mockopenai"
)

func TestS25ResponsesSubset(t *testing.T) {
	upstream := mockopenai.New()
	defer upstream.Close()
	app := newApp(t, t.TempDir())
	defer app.Close(context.Background())
	adminServer := httptest.NewServer(app.AdminHandler())
	defer adminServer.Close()
	dataServer := httptest.NewServer(app.DataHandler())
	defer dataServer.Close()
	admin := newAdminClient(t, adminServer.URL, app.AdminToken())
	key := app.LocalKeyOnce()
	_, modelID := setupProvider(t, admin, upstream, "responses")
	admin.must(http.MethodPatch, "/admin/models/"+modelID, map[string]any{"enabled": true}, http.StatusOK)

	body := `{"model":"responses/mock-small","input":"hello"}`
	request, _ := http.NewRequest(http.MethodPost, dataServer.URL+"/v1/responses", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(`"object":"response"`)) || !bytes.Contains(raw, []byte("hello from mock")) {
		t.Fatalf("response status=%d body=%s", response.StatusCode, raw)
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil || payload["status"] != "completed" {
		t.Fatalf("bad response object: %s", raw)
	}

	streamBody := `{"model":"responses/mock-small","input":[{"role":"user","content":"hello"}],"stream":true}`
	request, _ = http.NewRequest(http.MethodPost, dataServer.URL+"/v1/responses", strings.NewReader(streamBody))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	streamResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	streamRaw, _ := io.ReadAll(streamResponse.Body)
	if streamResponse.StatusCode != http.StatusOK ||
		!bytes.Contains(streamRaw, []byte("response.output_text.delta")) ||
		!bytes.Contains(streamRaw, []byte("response.completed")) {
		t.Fatalf("bad response SSE: status=%d body=%s", streamResponse.StatusCode, streamRaw)
	}

	unsupported := `{"model":"responses/mock-small","input":"hello","tools":[]}`
	request, _ = http.NewRequest(http.MethodPost, dataServer.URL+"/v1/responses", strings.NewReader(unsupported))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	unsupportedResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer unsupportedResponse.Body.Close()
	unsupportedRaw, _ := io.ReadAll(unsupportedResponse.Body)
	if unsupportedResponse.StatusCode != http.StatusUnprocessableEntity ||
		!bytes.Contains(unsupportedRaw, []byte("unsupported_response_field:tools")) {
		t.Fatalf("unsupported fields were not rejected: %d %s", unsupportedResponse.StatusCode, unsupportedRaw)
	}
}
