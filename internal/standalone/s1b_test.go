package standalone_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"agentmesh/internal/mockopenai"
)

type s1bEnv struct {
	admin    *adminClient
	dataURL  string
	upstream *mockopenai.Server
	model    string
}

func newS1BEnv(t *testing.T) *s1bEnv {
	t.Helper()
	upstream := mockopenai.New()
	t.Cleanup(upstream.Close)

	app := newApp(t, t.TempDir())
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("close app: %v", err)
		}
	})
	dataServer := httptest.NewServer(app.DataHandler())
	t.Cleanup(dataServer.Close)
	adminServer := httptest.NewServer(app.AdminHandler())
	t.Cleanup(adminServer.Close)

	admin := newAdminClient(t, adminServer.URL, app.AdminToken())
	setupProvider(t, admin, upstream, "s1b")
	return &s1bEnv{
		admin:    admin,
		dataURL:  dataServer.URL,
		upstream: upstream,
		model:    "s1b/" + mockopenai.SmallModel,
	}
}

type s1bClientKey struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Prefix         string     `json:"prefix"`
	PlaintextKey   string     `json:"plaintext_key"`
	Enabled        bool       `json:"enabled"`
	RPM            int        `json:"rpm"`
	DailyRequests  int        `json:"daily_requests"`
	DailyTokens    int        `json:"daily_tokens"`
	ModelAllowlist []string   `json:"model_allowlist"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
}

func s1bCreateKey(t *testing.T, admin *adminClient, input map[string]any) s1bClientKey {
	t.Helper()
	raw := admin.must(http.MethodPost, "/admin/client-keys", input, http.StatusCreated)
	var key s1bClientKey
	if err := json.Unmarshal(raw, &key); err != nil {
		t.Fatal(err)
	}
	if key.ID == "" || key.PlaintextKey == "" {
		t.Fatalf("client key create response missing id or plaintext: %s", raw)
	}
	return key
}

func s1bListKeys(t *testing.T, admin *adminClient) []s1bClientKey {
	t.Helper()
	raw := admin.must(http.MethodGet, "/admin/client-keys", nil, http.StatusOK)
	var payload struct {
		Data []s1bClientKey `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Data
}

func s1bFindKey(t *testing.T, keys []s1bClientKey, id string) s1bClientKey {
	t.Helper()
	for _, key := range keys {
		if key.ID == id {
			return key
		}
	}
	t.Fatalf("client key %s not found in list", id)
	return s1bClientKey{}
}

func s1bChat(t *testing.T, env *s1bEnv, label, key string, wantStatus int) string {
	t.Helper()
	status, body := chatRequest(t, env.dataURL, key, env.model)
	if status != wantStatus {
		t.Fatalf("%s chat status=%d want=%d body=%s", label, status, wantStatus, body)
	}
	return body
}

func s1bExpectChatOK(t *testing.T, env *s1bEnv, label, key string) {
	t.Helper()
	body := s1bChat(t, env, label, key, http.StatusOK)
	if !strings.Contains(body, "hello from mock") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("%s returned bad SSE: %s", label, body)
	}
}

func TestS1BAppKeyIsolationRevocationAndRotation(t *testing.T) {
	env := newS1BEnv(t)
	deviceA := s1bCreateKey(t, env.admin, map[string]any{"name": "device-a"})
	deviceB := s1bCreateKey(t, env.admin, map[string]any{"name": "device-b"})
	if !deviceA.Enabled || !deviceB.Enabled {
		t.Fatal("new app keys must start enabled")
	}

	env.upstream.Reset()
	usedAfter := time.Now().UTC().Add(-time.Second)
	s1bExpectChatOK(t, env, "device A", deviceA.PlaintextKey)
	s1bExpectChatOK(t, env, "device B", deviceB.PlaintextKey)
	if got := env.upstream.Requests(); got != 2 {
		t.Fatalf("upstream requests=%d want=2", got)
	}

	keys := s1bListKeys(t, env.admin)
	for _, created := range []s1bClientKey{deviceA, deviceB} {
		listed := s1bFindKey(t, keys, created.ID)
		if !listed.Enabled {
			t.Fatalf("%s must remain enabled after use", created.Name)
		}
		if listed.Prefix != created.Prefix {
			t.Fatalf("%s prefix=%q want=%q", created.Name, listed.Prefix, created.Prefix)
		}
		if listed.LastUsedAt == nil {
			t.Fatalf("%s last_used_at was not updated", created.Name)
		}
		if listed.LastUsedAt.Before(usedAfter) {
			t.Fatalf("%s last_used_at=%s predates the chat request", created.Name, listed.LastUsedAt)
		}
		if listed.PlaintextKey != "" {
			t.Fatalf("%s plaintext key leaked through the list", created.Name)
		}
	}

	raw := env.admin.must(http.MethodPost, "/admin/client-keys/"+deviceA.ID+"/rotate", nil, http.StatusCreated)
	var rotated s1bClientKey
	if err := json.Unmarshal(raw, &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.ID == "" || rotated.ID == deviceA.ID || rotated.PlaintextKey == "" {
		t.Fatalf("rotation did not return a fresh key identity and plaintext: %s", raw)
	}
	if rotated.PlaintextKey == deviceA.PlaintextKey {
		t.Fatal("rotation reused the old plaintext key")
	}
	if !rotated.Enabled || rotated.LastUsedAt != nil {
		t.Fatalf("rotated key initial state enabled=%v last_used_at=%v", rotated.Enabled, rotated.LastUsedAt)
	}

	keys = s1bListKeys(t, env.admin)
	oldA := s1bFindKey(t, keys, deviceA.ID)
	if oldA.Enabled {
		t.Fatal("old key remained enabled after rotation")
	}
	if oldA.PlaintextKey != "" {
		t.Fatal("old key plaintext leaked through the list")
	}
	newA := s1bFindKey(t, keys, rotated.ID)
	if !newA.Enabled || newA.LastUsedAt != nil {
		t.Fatalf("rotated key listed state enabled=%v last_used_at=%v", newA.Enabled, newA.LastUsedAt)
	}
	if newA.Prefix != rotated.Prefix {
		t.Fatalf("rotated key prefix=%q want=%q", newA.Prefix, rotated.Prefix)
	}
	if listedB := s1bFindKey(t, keys, deviceB.ID); !listedB.Enabled {
		t.Fatal("rotating device A affected device B")
	}

	s1bChat(t, env, "old device A", deviceA.PlaintextKey, http.StatusUnauthorized)
	rotatedUsedAfter := time.Now().UTC().Add(-time.Second)
	s1bExpectChatOK(t, env, "rotated device A", rotated.PlaintextKey)
	s1bExpectChatOK(t, env, "device B after rotation", deviceB.PlaintextKey)

	keys = s1bListKeys(t, env.admin)
	newA = s1bFindKey(t, keys, rotated.ID)
	if newA.LastUsedAt == nil {
		t.Fatal("rotated key last_used_at was not updated")
	}
	if newA.LastUsedAt.Before(rotatedUsedAfter) {
		t.Fatalf("rotated key last_used_at=%s predates its chat request", newA.LastUsedAt)
	}

	env.admin.must(http.MethodDelete, "/admin/client-keys/"+deviceB.ID, nil, http.StatusOK)
	s1bChat(t, env, "revoked device B", deviceB.PlaintextKey, http.StatusUnauthorized)
	s1bExpectChatOK(t, env, "rotated device A after revoke", rotated.PlaintextKey)

	keys = s1bListKeys(t, env.admin)
	revokedB := s1bFindKey(t, keys, deviceB.ID)
	if revokedB.Enabled {
		t.Fatal("revoked key remained enabled")
	}
	if revokedB.LastUsedAt == nil {
		t.Fatal("revocation erased the key's last_used_at history")
	}
	if !s1bFindKey(t, keys, rotated.ID).Enabled {
		t.Fatal("revoking device B affected rotated device A")
	}
	if s1bFindKey(t, keys, deviceA.ID).Enabled {
		t.Fatal("old device A key was unexpectedly re-enabled")
	}
}

func TestS1BClientKeyMetadataAndDailyRequestLimit(t *testing.T) {
	env := newS1BEnv(t)
	createdExpiry := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	createdAllowlist := []string{env.model}
	created := s1bCreateKey(t, env.admin, map[string]any{
		"name":            "metadata-device",
		"rpm":             30,
		"daily_requests":  5,
		"daily_tokens":    60000,
		"model_allowlist": createdAllowlist,
		"expires_at":      createdExpiry,
	})
	if created.RPM != 30 || created.DailyRequests != 5 || created.DailyTokens != 60000 {
		t.Fatalf("created limits rpm=%d daily_requests=%d daily_tokens=%d",
			created.RPM, created.DailyRequests, created.DailyTokens)
	}
	if !slices.Equal(created.ModelAllowlist, createdAllowlist) {
		t.Fatalf("created allowlist=%v want=%v", created.ModelAllowlist, createdAllowlist)
	}
	if created.ExpiresAt == nil || !created.ExpiresAt.Equal(createdExpiry) {
		t.Fatalf("created expires_at=%v want=%v", created.ExpiresAt, createdExpiry)
	}

	listed := s1bFindKey(t, s1bListKeys(t, env.admin), created.ID)
	if listed.RPM != created.RPM || listed.DailyRequests != created.DailyRequests || listed.DailyTokens != created.DailyTokens {
		t.Fatalf("listed limits rpm=%d daily_requests=%d daily_tokens=%d",
			listed.RPM, listed.DailyRequests, listed.DailyTokens)
	}
	if !slices.Equal(listed.ModelAllowlist, created.ModelAllowlist) {
		t.Fatalf("listed allowlist=%v want=%v", listed.ModelAllowlist, created.ModelAllowlist)
	}
	if listed.ExpiresAt == nil || !listed.ExpiresAt.Equal(createdExpiry) {
		t.Fatalf("listed expires_at=%v want=%v", listed.ExpiresAt, createdExpiry)
	}

	updatedExpiry := createdExpiry.Add(24 * time.Hour)
	updatedAllowlist := []string{"s1b/*"}
	env.admin.must(http.MethodPatch, "/admin/client-keys/"+created.ID, map[string]any{
		"rpm":             7,
		"daily_requests":  1,
		"daily_tokens":    50000,
		"model_allowlist": updatedAllowlist,
		"expires_at":      updatedExpiry,
	}, http.StatusOK)

	listed = s1bFindKey(t, s1bListKeys(t, env.admin), created.ID)
	if listed.Name != "metadata-device" || !listed.Enabled {
		t.Fatalf("listed identity/state name=%q enabled=%v", listed.Name, listed.Enabled)
	}
	if listed.RPM != 7 || listed.DailyRequests != 1 || listed.DailyTokens != 50000 {
		t.Fatalf("updated limits rpm=%d daily_requests=%d daily_tokens=%d",
			listed.RPM, listed.DailyRequests, listed.DailyTokens)
	}
	if !slices.Equal(listed.ModelAllowlist, updatedAllowlist) {
		t.Fatalf("updated allowlist=%v want=%v", listed.ModelAllowlist, updatedAllowlist)
	}
	if listed.ExpiresAt == nil || !listed.ExpiresAt.Equal(updatedExpiry) {
		t.Fatalf("updated expires_at=%v want=%v", listed.ExpiresAt, updatedExpiry)
	}
	if listed.PlaintextKey != "" {
		t.Fatal("client key plaintext leaked through the list")
	}

	s1bExpectChatOK(t, env, "metadata device first request", created.PlaintextKey)
	body := s1bChat(t, env, "metadata device capped request", created.PlaintextKey, http.StatusTooManyRequests)
	if !strings.Contains(body, "quota_exhausted") {
		t.Fatalf("daily request limit body=%s", body)
	}
}
