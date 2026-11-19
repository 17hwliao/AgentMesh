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
	"agentmesh/internal/standalone"
)

func setupProvider(t *testing.T, admin *adminClient, upstream *mockopenai.Server, slug string) (string, string) {
	t.Helper()
	raw := admin.must(http.MethodPost, "/admin/providers", map[string]any{
		"slug": slug, "name": slug, "base_url": upstream.URL(), "auth_type": "none",
	}, http.StatusCreated)
	var provider map[string]any
	if err := json.Unmarshal(raw, &provider); err != nil {
		t.Fatal(err)
	}
	providerID, _ := provider["id"].(string)
	admin.must(http.MethodPost, "/admin/providers/"+providerID+"/discover", nil, http.StatusOK)
	modelsRaw := admin.must(http.MethodGet, "/admin/models", nil, http.StatusOK)
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(modelsRaw, &list); err != nil {
		t.Fatal(err)
	}
	for _, model := range list.Data {
		if model["public_id"] == slug+"/"+mockopenai.SmallModel {
			id, _ := model["id"].(string)
			admin.must(http.MethodPatch, "/admin/models/"+id, map[string]any{"enabled": true}, http.StatusOK)
			return providerID, id
		}
	}
	t.Fatalf("model %s not found", slug+"/"+mockopenai.SmallModel)
	return "", ""
}

func chatRequest(t *testing.T, url, key, model string) (int, string) {
	t.Helper()
	body := `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	request, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

func TestS2RoutingFallbackUsageAndLimits(t *testing.T) {
	dir := t.TempDir()
	primary := mockopenai.New()
	secondary := mockopenai.New()
	defer primary.Close()
	defer secondary.Close()
	app := newApp(t, dir)
	defer app.Close(context.Background())
	adminToken := app.AdminToken()
	localKey := app.LocalKeyOnce()
	dataServer := httptest.NewServer(app.DataHandler())
	adminServer := httptest.NewServer(app.AdminHandler())
	defer dataServer.Close()
	defer adminServer.Close()
	admin := newAdminClient(t, adminServer.URL, adminToken)

	primaryID, primaryModelID := setupProvider(t, admin, primary, "primary")
	secondaryID, secondaryModelID := setupProvider(t, admin, secondary, "secondary")
	_ = primaryID
	_ = secondaryID

	groupRaw := admin.must(http.MethodPost, "/admin/routing-groups", map[string]any{
		"name": "auto", "description": "local fallback", "enabled": true,
		"members": []map[string]any{
			{"model_id": primaryModelID, "priority": 0, "weight": 1, "enabled": true},
			{"model_id": secondaryModelID, "priority": 1, "weight": 1, "enabled": true},
		},
	}, http.StatusCreated)
	if !bytes.Contains(groupRaw, []byte(`"auto"`)) {
		t.Fatalf("bad group response: %s", groupRaw)
	}

	primary.Reset()
	secondary.Reset()
	primary.SetStatus(mockopenai.SmallModel, http.StatusTooManyRequests)
	status, body := chatRequest(t, dataServer.URL, localKey, "auto")
	if status != http.StatusOK || !strings.Contains(body, "[DONE]") {
		t.Fatalf("fallback status=%d body=%s", status, body)
	}
	if primary.Requests() != 1 || secondary.Requests() != 1 {
		t.Fatalf("unexpected upstream calls primary=%d secondary=%d", primary.Requests(), secondary.Requests())
	}
	primaryCalls := primary.Requests()
	status, body = chatRequest(t, dataServer.URL, localKey, "auto")
	if status != http.StatusOK || !strings.Contains(body, "[DONE]") {
		t.Fatalf("cooled route status=%d body=%s", status, body)
	}
	if primary.Requests() != primaryCalls {
		t.Fatalf("cooled provider was retried: %d -> %d", primaryCalls, primary.Requests())
	}

	usage := admin.must(http.MethodGet, "/admin/usage", nil, http.StatusOK)
	if !bytes.Contains(usage, []byte(`"reason":"rate_limited"`)) || !bytes.Contains(usage, []byte(`"requests":2`)) {
		t.Fatalf("usage/cooldown evidence missing: %s", usage)
	}

	limitedRaw := admin.must(http.MethodPost, "/admin/client-keys", map[string]any{
		"name": "limited", "rpm": 1,
	}, http.StatusCreated)
	var limited struct {
		Plaintext string `json:"plaintext_key"`
	}
	if err := json.Unmarshal(limitedRaw, &limited); err != nil || limited.Plaintext == "" {
		t.Fatalf("limited key create: %s", limitedRaw)
	}
	status, body = chatRequest(t, dataServer.URL, limited.Plaintext, "secondary/mock-small")
	if status != http.StatusOK {
		t.Fatalf("first limited call status=%d body=%s", status, body)
	}
	status, body = chatRequest(t, dataServer.URL, limited.Plaintext, "secondary/mock-small")
	if status != http.StatusTooManyRequests || !strings.Contains(body, "quota_exhausted") {
		t.Fatalf("rpm limit status=%d body=%s", status, body)
	}

	allowlistedRaw := admin.must(http.MethodPost, "/admin/client-keys", map[string]any{
		"name": "allowlisted", "model_allowlist": []string{"secondary/mock-small"},
	}, http.StatusCreated)
	var allowlisted struct {
		Plaintext string `json:"plaintext_key"`
	}
	_ = json.Unmarshal(allowlistedRaw, &allowlisted)
	status, _ = chatRequest(t, dataServer.URL, allowlisted.Plaintext, "primary/mock-small")
	if status != http.StatusForbidden {
		t.Fatalf("model allowlist status=%d", status)
	}

	// Restart still preserves the enabled catalog, key hashes, usage and
	// cooldown rows; the data plane can answer immediately.
	dataServer.Close()
	adminServer.Close()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := newApp(t, dir)
	defer restarted.Close(context.Background())
	data2 := httptest.NewServer(restarted.DataHandler())
	defer data2.Close()
	admin2Server := httptest.NewServer(restarted.AdminHandler())
	defer admin2Server.Close()
	admin2 := newAdminClient(t, admin2Server.URL, adminToken)
	status, body = chatRequest(t, data2.URL, localKey, "auto")
	if status != http.StatusOK || !strings.Contains(body, "hello from mock") {
		t.Fatalf("restart route status=%d body=%s", status, body)
	}
	usage2 := admin2.must(http.MethodGet, "/admin/usage", nil, http.StatusOK)
	if !bytes.Contains(usage2, []byte(`"rows"`)) {
		t.Fatalf("usage did not survive restart: %s", usage2)
	}
}

func TestS2MockStatusFallback(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			primary := mockopenai.New()
			secondary := mockopenai.New()
			defer primary.Close()
			defer secondary.Close()
			app := newApp(t, t.TempDir())
			defer app.Close(context.Background())
			adminServer := httptest.NewServer(app.AdminHandler())
			defer adminServer.Close()
			dataServer := httptest.NewServer(app.DataHandler())
			defer dataServer.Close()
			admin := newAdminClient(t, adminServer.URL, app.AdminToken())
			key := app.LocalKeyOnce()
			_, primaryModel := setupProvider(t, admin, primary, "primary")
			_, secondaryModel := setupProvider(t, admin, secondary, "secondary")
			admin.must(http.MethodPost, "/admin/routing-groups", map[string]any{
				"name": "auto", "members": []map[string]any{
					{"model_id": primaryModel, "priority": 0},
					{"model_id": secondaryModel, "priority": 1},
				},
			}, http.StatusCreated)
			primary.Reset()
			secondary.Reset()
			primary.SetStatus(mockopenai.SmallModel, status)
			code, body := chatRequest(t, dataServer.URL, key, "auto")
			if code != http.StatusOK || !strings.Contains(body, "hello from mock") {
				t.Fatalf("status fallback %d -> %d %s", status, code, body)
			}
		})
	}
}

func TestS2NoAuthUpstream(t *testing.T) {
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
	_, modelID := setupProvider(t, admin, upstream, "noauth")
	admin.must(http.MethodPatch, "/admin/models/"+modelID, map[string]any{"enabled": true}, http.StatusOK)
	code, body := chatRequest(t, dataServer.URL, key, "noauth/mock-small")
	if code != http.StatusOK || !strings.Contains(body, "hello from mock") {
		t.Fatalf("no-auth upstream status=%d body=%s", code, body)
	}
}

var _ = standalone.Config{}
