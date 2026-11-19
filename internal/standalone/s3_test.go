package standalone_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentmesh/internal/mockopenai"
)

func createDeviceKey(t *testing.T, admin *adminClient, name string) (string, string) {
	t.Helper()
	raw := admin.must(http.MethodPost, "/admin/client-keys", map[string]any{"name": name}, http.StatusCreated)
	var payload struct {
		ID       string `json:"id"`
		PlainKey string `json:"plaintext_key"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID == "" || payload.PlainKey == "" {
		t.Fatalf("incomplete device key response: %s", raw)
	}
	return payload.ID, payload.PlainKey
}

func createRemoteProvider(t *testing.T, admin *adminClient, slug, baseURL, apiKey string, tlsInsecure bool) string {
	t.Helper()
	raw := admin.must(http.MethodPost, "/admin/providers", map[string]any{
		"slug": slug, "name": slug, "kind": "agentmesh", "base_url": baseURL,
		"auth_type": "bearer", "api_key": apiKey, "tls_insecure": tlsInsecure, "enabled": true,
	}, http.StatusCreated)
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ID == "" {
		t.Fatalf("missing provider id: %s", raw)
	}
	return payload.ID
}

func discoverAndEnableModel(t *testing.T, admin *adminClient, providerID, publicID string) string {
	t.Helper()
	admin.must(http.MethodPost, "/admin/providers/"+providerID+"/discover", nil, http.StatusOK)
	raw := admin.must(http.MethodGet, "/admin/models", nil, http.StatusOK)
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, model := range payload.Data {
		if model["public_id"] == publicID {
			id, _ := model["id"].(string)
			if id == "" {
				t.Fatalf("model %s has no id", publicID)
			}
			admin.must(http.MethodPatch, "/admin/models/"+id, map[string]any{"enabled": true}, http.StatusOK)
			return id
		}
	}
	t.Fatalf("model %s not found in %s", publicID, raw)
	return ""
}

func TestS3RemoteAgentMeshTLSAndDeviceKeyIsolation(t *testing.T) {
	upstream := mockopenai.New()
	defer upstream.Close()

	appA := newApp(t, t.TempDir())
	defer appA.Close(context.Background())
	adminAServer := httptest.NewServer(appA.AdminHandler())
	defer adminAServer.Close()
	dataAServer := httptest.NewTLSServer(appA.DataHandler())
	defer dataAServer.Close()
	adminA := newAdminClient(t, adminAServer.URL, appA.AdminToken())

	setupProvider(t, adminA, upstream, "upstream")
	deviceAID, deviceAKey := createDeviceKey(t, adminA, "device-a")
	deviceBID, deviceBKey := createDeviceKey(t, adminA, "device-b")

	appB := newApp(t, t.TempDir())
	defer appB.Close(context.Background())
	adminBServer := httptest.NewServer(appB.AdminHandler())
	defer adminBServer.Close()
	dataBServer := httptest.NewServer(appB.DataHandler())
	defer dataBServer.Close()
	adminB := newAdminClient(t, adminBServer.URL, appB.AdminToken())
	localKeyB := appB.LocalKeyOnce()
	if localKeyB == "" {
		t.Fatal("missing local B key")
	}

	plainProviderID := createRemoteProvider(t, adminB, "plain", dataAServer.URL+"/v1", deviceAKey, false)
	status, _ := adminB.do(http.MethodPost, "/admin/providers/"+plainProviderID+"/discover", nil)
	if status != http.StatusBadGateway {
		t.Fatalf("TLS verification should fail without tls_insecure, status=%d", status)
	}

	officeProviderID := createRemoteProvider(t, adminB, "office", dataAServer.URL+"/v1", deviceAKey, true)
	branchProviderID := createRemoteProvider(t, adminB, "branch", dataAServer.URL+"/v1", deviceBKey, true)

	data, err := appB.Manager().ListData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foundTLS := false
	for _, provider := range data.Providers {
		if provider.ID == officeProviderID && provider.TLSInsecure {
			foundTLS = true
		}
	}
	if !foundTLS {
		t.Fatal("tls_insecure was not persisted on the remote provider")
	}

	officeModel := "office/upstream/" + mockopenai.SmallModel
	branchModel := "branch/upstream/" + mockopenai.SmallModel
	discoverAndEnableModel(t, adminB, officeProviderID, officeModel)
	discoverAndEnableModel(t, adminB, branchProviderID, branchModel)

	if status, body := chatRequest(t, dataBServer.URL, localKeyB, officeModel); status != http.StatusOK || body == "" {
		t.Fatalf("remote office call failed: status=%d body=%s", status, body)
	}
	if status, body := chatRequest(t, dataBServer.URL, localKeyB, branchModel); status != http.StatusOK || body == "" {
		t.Fatalf("remote branch call failed: status=%d body=%s", status, body)
	}

	adminA.must(http.MethodDelete, "/admin/client-keys/"+deviceAID, nil, http.StatusOK)
	if status, body := chatRequest(t, dataBServer.URL, localKeyB, officeModel); status == http.StatusOK {
		t.Fatalf("revoked device key still worked: %s", body)
	}
	if status, body := chatRequest(t, dataBServer.URL, localKeyB, branchModel); status != http.StatusOK || body == "" {
		t.Fatalf("independent device key failed after revoke: status=%d body=%s", status, body)
	}

	keysRaw := adminA.must(http.MethodGet, "/admin/client-keys", nil, http.StatusOK)
	var keys struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(keysRaw, &keys); err != nil {
		t.Fatal(err)
	}
	seenB := false
	for _, key := range keys.Data {
		switch key["id"] {
		case deviceAID:
			if key["enabled"] != false {
				t.Fatalf("revoked device A still enabled: %v", key)
			}
		case deviceBID:
			seenB = true
			if key["enabled"] != true {
				t.Fatalf("device B was affected by revoke: %v", key)
			}
		}
	}
	if !seenB {
		t.Fatal("device B key missing from catalog")
	}
}
