package standalone_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agentmesh/internal/catalog"
	"agentmesh/internal/mockopenai"
)

func TestS35ALocalModelDiscoveryAndImport(t *testing.T) {
	for _, fixture := range []struct {
		kind, discoveryPath, initial, updated, empty string
	}{
		{"ollama", "/api/tags", `{"models":[{"name":"mock-small"},{"name":"mock-vision"}]}`, `{"models":[{"name":"mock-small"}]}`, `{"models":[]}`},
		{"lmstudio", "/v1/models", `{"data":[{"id":"mock-small"},{"id":"mock-vision"}]}`, `{"data":[{"id":"mock-small"}]}`, `{"data":[]}`},
	} {
		t.Run(fixture.kind, func(t *testing.T) {
			var directory atomic.Int32
			var chats atomic.Int32
			mock := &mockopenai.Server{}
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("local provider received an Authorization header")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch {
				case r.Method == http.MethodGet && r.URL.Path == fixture.discoveryPath:
					switch directory.Load() {
					case 0:
						_, _ = io.WriteString(w, fixture.initial)
					case 1:
						_, _ = io.WriteString(w, fixture.empty)
					case 2:
						http.Error(w, "upstream-private-detail", http.StatusServiceUnavailable)
					case 3:
						_, _ = io.WriteString(w, fixture.updated)
					}
				case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
					chats.Add(1)
					mock.ServeHTTP(w, r)
				default:
					http.NotFound(w, r)
				}
			}))
			defer local.Close()

			dir := t.TempDir()
			app := newApp(t, dir)
			defer app.Close(context.Background())
			adminToken, localKey := app.AdminToken(), app.LocalKeyOnce()
			adminServer := httptest.NewServer(app.AdminHandler())
			defer adminServer.Close()
			dataServer := httptest.NewServer(app.DataHandler())
			defer dataServer.Close()
			admin := newAdminClient(t, adminServer.URL, adminToken)
			input := map[string]any{"kind": fixture.kind, "base_url": local.URL}
			const importPath = "/admin/host/local-models/import"
			scan := admin.must(http.MethodPost, "/admin/host/local-models/scan", input, http.StatusOK)
			if !bytes.Contains(scan, []byte(mockopenai.SmallModel)) || !bytes.Contains(scan, []byte(`"reachable":true`)) {
				t.Fatalf("local scan response = %s", scan)
			}
			admin.must(http.MethodPost, importPath, input, http.StatusUnprocessableEntity)
			input["confirm"] = true
			imported := admin.must(http.MethodPost, importPath, input, http.StatusCreated)
			var payload struct {
				Provider catalog.Provider `json:"provider"`
				Models   []string         `json:"models"`
			}
			if err := json.Unmarshal(imported, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Provider.Kind != fixture.kind || payload.Provider.AuthType != catalog.AuthNone ||
				payload.Provider.BaseURL != local.URL+"/v1" || len(payload.Models) != 2 {
				t.Fatalf("unexpected import: %s", imported)
			}
			small := fixture.kind + "/" + mockopenai.SmallModel
			vision := fixture.kind + "/" + mockopenai.VisionModel
			modelPath := "/admin/models?provider=" + payload.Provider.ID
			models := localCatalogModels(t, admin, modelPath)
			for _, id := range []string{small, vision} {
				if models[id].ID == "" || models[id].Enabled || models[id].Stale {
					t.Fatalf("expected disabled model %q: %+v", id, models[id])
				}
			}
			if status, _ := chatRequest(t, dataServer.URL, localKey, small); status == http.StatusOK || chats.Load() != 0 {
				t.Fatalf("disabled model reached upstream: status=%d, chats=%d", status, chats.Load())
			}
			admin.must(http.MethodPatch, "/admin/models/"+models[small].ID, map[string]any{"enabled": true, "tools": true}, http.StatusOK)
			admin.must(http.MethodPatch, "/admin/models/"+models[vision].ID, map[string]any{"enabled": true}, http.StatusOK)
			assertLocalSSE(t, dataServer.URL, localKey, small)
			if chats.Load() != 1 {
				t.Fatalf("initial chat request count = %d", chats.Load())
			}

			for _, failure := range []struct {
				state  int32
				status int
				code   string
			}{
				{1, http.StatusUnprocessableEntity, "local_models_empty"},
				{2, http.StatusBadGateway, "local_models_status"},
			} {
				directory.Store(failure.state)
				raw := admin.must(http.MethodPost, importPath, input, failure.status)
				if !bytes.Contains(raw, []byte(failure.code)) || bytes.Contains(raw, []byte("upstream-private-detail")) {
					t.Fatalf("unexpected import failure: %s", raw)
				}
				unchanged := localCatalogModels(t, admin, modelPath)
				if !unchanged[vision].Enabled || unchanged[vision].Stale || !unchanged[small].Tools {
					t.Fatalf("failed import changed catalog: %+v", unchanged)
				}
			}
			directory.Store(3)
			admin.must(http.MethodPost, importPath, input, http.StatusCreated)
			checkRefreshed := func(admin *adminClient) {
				current := localCatalogModels(t, admin, modelPath)
				if current[small].ID != models[small].ID || !current[small].Enabled || !current[small].Tools || current[small].Stale ||
					current[vision].ID != models[vision].ID || !current[vision].Enabled || !current[vision].Stale {
					t.Fatalf("unexpected refreshed catalog: %+v", current)
				}
			}
			checkRefreshed(admin)
			if status, _ := chatRequest(t, dataServer.URL, localKey, vision); status == http.StatusOK || chats.Load() != 1 {
				t.Fatalf("stale model reached upstream: status=%d, chats=%d", status, chats.Load())
			}
			dataServer.Close()
			adminServer.Close()
			if err := app.Close(context.Background()); err != nil {
				t.Fatal(err)
			}

			restarted := newApp(t, dir)
			defer restarted.Close(context.Background())
			adminAfter := httptest.NewServer(restarted.AdminHandler())
			defer adminAfter.Close()
			dataAfter := httptest.NewServer(restarted.DataHandler())
			defer dataAfter.Close()
			checkRefreshed(newAdminClient(t, adminAfter.URL, adminToken))
			assertLocalSSE(t, dataAfter.URL, localKey, small)
			if chats.Load() != 2 {
				t.Fatalf("restarted chat request count = %d", chats.Load())
			}
		})
	}
}

func assertLocalSSE(t *testing.T, baseURL, key, model string) {
	t.Helper()
	status, raw := chatRequest(t, baseURL, key, model)
	if status != http.StatusOK || !bytes.Contains([]byte(raw), []byte("hello from mock")) || !bytes.Contains([]byte(raw), []byte("[DONE]")) {
		t.Fatalf("chat status=%d body=%s", status, raw)
	}
}

func localCatalogModels(t *testing.T, admin *adminClient, path string) map[string]catalog.Model {
	t.Helper()
	var payload struct {
		Data []catalog.Model `json:"data"`
	}
	if err := json.Unmarshal(admin.must(http.MethodGet, path, nil, http.StatusOK), &payload); err != nil {
		t.Fatal(err)
	}
	models := make(map[string]catalog.Model, len(payload.Data))
	for _, model := range payload.Data {
		models[model.PublicID] = model
	}
	return models
}

func TestS35ARejectsNonLoopbackImport(t *testing.T) {
	app := newApp(t, t.TempDir())
	defer app.Close(context.Background())
	adminServer := httptest.NewServer(app.AdminHandler())
	defer adminServer.Close()
	admin := newAdminClient(t, adminServer.URL, app.AdminToken())
	status, body := admin.do(http.MethodPost, "/admin/host/local-models/import", map[string]any{
		"kind": "ollama", "base_url": "http://192.0.2.10:11434", "confirm": true,
	})
	if status != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte("local_models_endpoint_must_be_loopback")) {
		t.Fatalf("non-loopback status=%d body=%s", status, body)
	}
	admin.must(http.MethodPost, "/admin/providers", map[string]any{
		"kind": "ollama", "slug": "local", "name": "Local", "auth_type": "none", "base_url": "http://192.0.2.10:11434/v1",
	}, http.StatusUnprocessableEntity)
}

func TestS35AHostScanPermissionsAndMetadata(t *testing.T) {
	root := t.TempDir()
	for _, variable := range []string{"APPDATA", "LOCALAPPDATA", "PATH"} {
		t.Setenv(variable, "")
	}
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	app := newApp(t, t.TempDir())
	defer app.Close(context.Background())
	server := httptest.NewServer(app.AdminHandler())
	defer server.Close()
	response, err := http.Get(server.URL + "/admin/host/agents")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated host status = %d", response.StatusCode)
	}

	admin := newAdminClient(t, server.URL, app.AdminToken())
	csrf := admin.csrf
	admin.csrf = ""
	admin.must(http.MethodPost, "/admin/host/scan", nil, http.StatusForbidden)
	admin.csrf = csrf
	request, err := http.NewRequest(http.MethodPost, server.URL+"/admin/host/scan", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Origin", "https://other-origin.example")
	response, err = admin.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin host status = %d", response.StatusCode)
	}

	directory := filepath.Join(root, ".codex")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "host-config-private-fixture"
	if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := admin.must(http.MethodPost, "/admin/host/scan", map[string]any{"extra_roots": []string{root}}, http.StatusOK)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte("auth.json")) {
		t.Fatal("host scan returned config content or credential file metadata")
	}
	var result struct {
		Agents []struct {
			ID          string   `json:"id"`
			ConfigPaths []string `json:"config_paths"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	for _, agent := range result.Agents {
		for _, path := range agent.ConfigPaths {
			if agent.ID == "codex" && path == directory {
				return
			}
		}
	}
	t.Fatal("host scan did not report the fixture directory")
}
