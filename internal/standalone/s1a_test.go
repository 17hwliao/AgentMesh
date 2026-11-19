package standalone_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentmesh/internal/mockopenai"
	"agentmesh/internal/standalone"
)

type adminClient struct {
	t      *testing.T
	client *http.Client
	base   string
	csrf   string
}

func newAdminClient(t *testing.T, adminURL, token string) *adminClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	body, _ := json.Marshal(map[string]string{"token": token})
	response, err := client.Post(adminURL+"/admin/session", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("login status=%d body=%s", response.StatusCode, raw)
	}
	var payload struct {
		CSRF string `json:"csrf"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return &adminClient{t: t, client: client, base: adminURL, csrf: payload.CSRF}
}

func (c *adminClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, raw
}

func (c *adminClient) must(method, path string, body any, want int) []byte {
	c.t.Helper()
	status, raw := c.do(method, path, body)
	if status != want {
		c.t.Fatalf("%s %s status=%d want=%d body=%s", method, path, status, want, raw)
	}
	return raw
}

func newApp(t *testing.T, dir string) *standalone.App {
	t.Helper()
	app, err := standalone.New(context.Background(), standalone.Config{
		DataDir: dir, DataAddr: "127.0.0.1:0", AdminAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestS1AClosedLoopAndRestart(t *testing.T) {
	dir := t.TempDir()
	upstream := mockopenai.New()
	defer upstream.Close()
	upstream.SetAuthToken("upstream-secret")

	app := newApp(t, dir)
	adminToken := app.AdminToken()
	localKey := app.LocalKeyOnce()
	if adminToken == "" || localKey == "" {
		t.Fatal("first start must expose one-time admin token and local key")
	}
	dataServer := httptest.NewServer(app.DataHandler())
	adminServer := httptest.NewServer(app.AdminHandler())
	defer dataServer.Close()
	defer adminServer.Close()

	admin := newAdminClient(t, adminServer.URL, adminToken)
	state := admin.must(http.MethodGet, "/admin/state", nil, http.StatusOK)
	var statePayload map[string]any
	if err := json.Unmarshal(state, &statePayload); err != nil {
		t.Fatal(err)
	}
	if statePayload["ready"] == true {
		t.Fatal("empty catalog must not be ready")
	}

	provider := admin.must(http.MethodPost, "/admin/providers", map[string]any{
		"slug": "mock", "name": "Mock Upstream", "base_url": upstream.URL(),
		"auth_type": "bearer", "api_key": "upstream-secret",
	}, http.StatusCreated)
	var providerPayload map[string]any
	_ = json.Unmarshal(provider, &providerPayload)
	providerID, _ := providerPayload["id"].(string)
	if providerID == "" {
		t.Fatal("provider id missing")
	}

	admin.must(http.MethodPost, "/admin/providers/"+providerID+"/test", map[string]any{"kind": "models"}, http.StatusOK)
	discovered := admin.must(http.MethodPost, "/admin/providers/"+providerID+"/discover", nil, http.StatusOK)
	if !bytes.Contains(discovered, []byte("mock-small")) {
		t.Fatalf("discovery did not return mock-small: %s", discovered)
	}

	modelsRaw := admin.must(http.MethodGet, "/admin/models?provider="+providerID, nil, http.StatusOK)
	var models struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(modelsRaw, &models); err != nil {
		t.Fatal(err)
	}
	var modelID string
	for _, model := range models.Data {
		if model["public_id"] == "mock/mock-small" {
			modelID, _ = model["id"].(string)
			if model["enabled"] != false {
				t.Fatal("discovered model must default disabled")
			}
		}
	}
	if modelID == "" {
		t.Fatal("mock/mock-small not found")
	}
	admin.must(http.MethodPatch, "/admin/models/"+modelID, map[string]any{"enabled": true}, http.StatusOK)

	ready, err := http.Get(dataServer.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	ready.Body.Close()
	if ready.StatusCode != http.StatusOK {
		t.Fatalf("ready status=%d", ready.StatusCode)
	}

	chat := func(key string) string {
		body := `{"model":"mock/mock-small","stream":true,"messages":[{"role":"user","content":"hello"}]}`
		request, _ := http.NewRequest(http.MethodPost, dataServer.URL+"/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("chat status=%d body=%s", response.StatusCode, raw)
		}
		return string(raw)
	}
	if got := chat(localKey); !strings.Contains(got, "hello from mock") || !strings.Contains(got, "[DONE]") {
		t.Fatalf("bad SSE: %s", got)
	}

	providerSecretScan, _ := os.ReadFile(filepath.Join(dir, "catalog.db"))
	if bytes.Contains(providerSecretScan, []byte("upstream-secret")) {
		t.Fatal("plaintext provider credential found in catalog.db")
	}
	adminList := admin.must(http.MethodGet, "/admin/providers", nil, http.StatusOK)
	if bytes.Contains(adminList, []byte("upstream-secret")) {
		t.Fatal("provider credential leaked through admin response")
	}

	// A rejected mutation must leave the old snapshot and generation serving.
	before := admin.must(http.MethodGet, "/admin/state", nil, http.StatusOK)
	badStatus, _ := admin.do(http.MethodPatch, "/admin/providers/"+providerID, map[string]any{
		"base_url": "http://169.254.169.254/v1",
	})
	if badStatus != http.StatusUnprocessableEntity {
		t.Fatalf("forbidden metadata URL status=%d", badStatus)
	}
	after := admin.must(http.MethodGet, "/admin/state", nil, http.StatusOK)
	if !bytes.Equal(before, after) {
		t.Fatalf("state changed after rejected mutation\nbefore=%s\nafter=%s", before, after)
	}
	if got := chat(localKey); !strings.Contains(got, "hello from mock") {
		t.Fatalf("old snapshot stopped serving after rejected mutation: %s", got)
	}

	dataServer.Close()
	adminServer.Close()
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	restarted := newApp(t, dir)
	defer restarted.Close(context.Background())
	restartedData := httptest.NewServer(restarted.DataHandler())
	defer restartedData.Close()
	body := `{"model":"mock/mock-small","stream":true,"messages":[{"role":"user","content":"after restart"}]}`
	request, _ := http.NewRequest(http.MethodPost, restartedData.URL+"/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+localKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(raw), "hello from mock") {
		t.Fatalf("restart chat status=%d body=%s", response.StatusCode, raw)
	}
}

func TestS1ARepairModeAfterPersistedBadConfig(t *testing.T) {
	dir := t.TempDir()
	upstream := mockopenai.New()
	defer upstream.Close()
	app := newApp(t, dir)
	adminToken := app.AdminToken()
	adminServer := httptest.NewServer(app.AdminHandler())
	defer adminServer.Close()
	admin := newAdminClient(t, adminServer.URL, adminToken)
	provider := admin.must(http.MethodPost, "/admin/providers", map[string]any{
		"slug": "mock", "name": "Mock", "base_url": upstream.URL(), "auth_type": "bearer", "api_key": "token",
	}, http.StatusCreated)
	var p map[string]any
	_ = json.Unmarshal(provider, &p)
	id, _ := p["id"].(string)
	admin.must(http.MethodPost, "/admin/providers/"+id+"/discover", nil, http.StatusOK)
	_ = app.Close(context.Background())

	db, err := openSQLite(filepath.Join(dir, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE providers SET base_url='ftp://bad' WHERE id=?`, id); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	restarted := newApp(t, dir)
	defer restarted.Close(context.Background())
	dataServer := httptest.NewServer(restarted.DataHandler())
	defer dataServer.Close()
	adminServer2 := httptest.NewServer(restarted.AdminHandler())
	defer adminServer2.Close()
	admin2 := newAdminClient(t, adminServer2.URL, adminToken)
	state := admin2.must(http.MethodGet, "/admin/state", nil, http.StatusOK)
	if !bytes.Contains(state, []byte(`"repair":"snapshot_invalid"`)) {
		t.Fatalf("repair state not visible: %s", state)
	}
	ready, err := http.Get(dataServer.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	ready.Body.Close()
	if ready.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("repair mode ready status=%d", ready.StatusCode)
	}
	admin2.must(http.MethodPatch, "/admin/providers/"+id, map[string]any{"base_url": upstream.URL()}, http.StatusOK)
	ready, err = http.Get(dataServer.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	ready.Body.Close()
	if ready.StatusCode == http.StatusServiceUnavailable {
		// No model is enabled in this fixture; repair itself is proven by the
		// snapshot returning to a readable generation, not by a model count.
		state2 := admin2.must(http.MethodGet, "/admin/state", nil, http.StatusOK)
		if bytes.Contains(state2, []byte(`"repair":"snapshot_invalid"`)) {
			t.Fatalf("repair did not clear: %s", state2)
		}
	}
}

func openSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
}
