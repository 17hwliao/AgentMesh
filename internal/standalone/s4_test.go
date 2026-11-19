package standalone_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agentmesh/internal/mockopenai"
	"agentmesh/internal/standalone"
)

func newAppConfig(t *testing.T, dir string, cfg standalone.Config) *standalone.App {
	t.Helper()
	cfg.DataDir = dir
	cfg.DataAddr = "127.0.0.1:0"
	cfg.AdminAddr = "127.0.0.1:0"
	app, err := standalone.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func chatWithClient(t *testing.T, client *http.Client, url, key, model string) (int, string) {
	t.Helper()
	body := `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	request, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(raw)
}

func TestS4LocalTLSAdminIsolationIPLimitAndAudit(t *testing.T) {
	upstream := mockopenai.New()
	defer upstream.Close()
	app := newAppConfig(t, t.TempDir(), standalone.Config{IPRequestsPerMinute: 1})
	defer app.Close(context.Background())
	adminServer := httptest.NewServer(app.AdminHandler())
	defer adminServer.Close()
	dataServer := httptest.NewTLSServer(app.DataHandler())
	defer dataServer.Close()
	admin := newAdminClient(t, adminServer.URL, app.AdminToken())
	localKey := app.LocalKeyOnce()
	setupProvider(t, admin, upstream, "upstream")

	if status := getStatus(t, dataServer.Client(), dataServer.URL+"/healthz"); status != http.StatusOK {
		t.Fatalf("healthz status=%d", status)
	}
	if status := getStatus(t, dataServer.Client(), dataServer.URL+"/readyz"); status != http.StatusOK {
		t.Fatalf("readyz status=%d", status)
	}
	if status := getStatus(t, dataServer.Client(), dataServer.URL+"/admin/state"); status != http.StatusNotFound {
		t.Fatalf("data plane exposed admin route, status=%d", status)
	}

	model := "upstream/" + mockopenai.SmallModel
	if status, body := chatWithClient(t, dataServer.Client(), dataServer.URL, localKey, model); status != http.StatusOK || body == "" {
		t.Fatalf("first TLS data request failed: status=%d body=%s", status, body)
	}
	if status, _ := chatWithClient(t, dataServer.Client(), dataServer.URL, localKey, model); status != http.StatusTooManyRequests {
		t.Fatalf("IP limit did not reject second request, status=%d", status)
	}

	raw := admin.must(http.MethodGet, "/admin/audit", nil, http.StatusOK)
	var audit struct {
		Data []standalone.AuditEvent `json:"data"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range audit.Data {
		if event.Action == "data_request" && event.Target == model && event.Outcome == "success" {
			found = true
			if !strings.Contains(event.Detail, "input_tokens=") || strings.Contains(event.Detail, "hello") {
				t.Fatalf("audit detail is not bounded: %q", event.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("data_request audit event not found: %s", raw)
	}
}

func getStatus(t *testing.T, client *http.Client, url string) int {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func TestS4ConcurrencyLimit(t *testing.T) {
	upstream := mockopenai.New()
	defer upstream.Close()
	app := newAppConfig(t, t.TempDir(), standalone.Config{MaxConcurrent: 1})
	defer app.Close(context.Background())
	adminServer := httptest.NewServer(app.AdminHandler())
	defer adminServer.Close()
	dataServer := httptest.NewServer(app.DataHandler())
	defer dataServer.Close()
	admin := newAdminClient(t, adminServer.URL, app.AdminToken())
	localKey := app.LocalKeyOnce()
	setupProvider(t, admin, upstream, "upstream")
	upstream.SetDelay(200 * time.Millisecond)
	model := "upstream/" + mockopenai.SmallModel

	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			status, _ := chatRequest(t, dataServer.URL, localKey, model)
			statuses <- status
		}()
	}
	close(start)
	wait.Wait()
	close(statuses)
	seenOK, seenLimited := false, false
	for status := range statuses {
		switch status {
		case http.StatusOK:
			seenOK = true
		case http.StatusTooManyRequests:
			seenLimited = true
		}
	}
	if !seenOK || !seenLimited {
		t.Fatalf("expected one success and one 429, got %v", statuses)
	}
}

func TestS4BodyAndDailyTokenLimits(t *testing.T) {
	upstream := mockopenai.New()
	defer upstream.Close()
	bodyApp := newAppConfig(t, t.TempDir(), standalone.Config{MaxBodyBytes: 32})
	defer bodyApp.Close(context.Background())
	bodyAdminServer := httptest.NewServer(bodyApp.AdminHandler())
	defer bodyAdminServer.Close()
	bodyDataServer := httptest.NewServer(bodyApp.DataHandler())
	defer bodyDataServer.Close()
	bodyAdmin := newAdminClient(t, bodyAdminServer.URL, bodyApp.AdminToken())
	bodyKey := bodyApp.LocalKeyOnce()
	setupProvider(t, bodyAdmin, upstream, "body-upstream")
	if status, _ := chatRequest(t, bodyDataServer.URL, bodyKey, "body-upstream/"+mockopenai.SmallModel); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit status=%d, want 413", status)
	}

	tokenApp := newAppConfig(t, t.TempDir(), standalone.Config{DailyTokenLimit: 1})
	defer tokenApp.Close(context.Background())
	tokenAdminServer := httptest.NewServer(tokenApp.AdminHandler())
	defer tokenAdminServer.Close()
	tokenDataServer := httptest.NewServer(tokenApp.DataHandler())
	defer tokenDataServer.Close()
	tokenAdmin := newAdminClient(t, tokenAdminServer.URL, tokenApp.AdminToken())
	tokenKey := tokenApp.LocalKeyOnce()
	setupProvider(t, tokenAdmin, upstream, "token-upstream")
	model := "token-upstream/" + mockopenai.SmallModel
	if status, body := chatRequest(t, tokenDataServer.URL, tokenKey, model); status != http.StatusOK || body == "" {
		t.Fatalf("first token-limited request failed: status=%d body=%s", status, body)
	}
	if status, _ := chatRequest(t, tokenDataServer.URL, tokenKey, model); status != http.StatusTooManyRequests {
		t.Fatalf("daily token limit did not reject second request, status=%d", status)
	}
}

func TestS4BackupAndRestore(t *testing.T) {
	upstream := mockopenai.New()
	defer upstream.Close()
	sourceDir := t.TempDir()
	backupDir := t.TempDir()
	restoredDir := t.TempDir()
	app := newAppConfig(t, sourceDir, standalone.Config{})
	adminServer := httptest.NewServer(app.AdminHandler())
	admin := newAdminClient(t, adminServer.URL, app.AdminToken())
	localKey := app.LocalKeyOnce()
	setupProvider(t, admin, upstream, "upstream")
	adminServer.Close()
	if err := app.Backup(context.Background(), backupDir); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := standalone.RestoreDataDir(backupDir, restoredDir); err != nil {
		t.Fatal(err)
	}
	restored := newAppConfig(t, restoredDir, standalone.Config{})
	defer restored.Close(context.Background())
	dataServer := httptest.NewServer(restored.DataHandler())
	defer dataServer.Close()
	if status, body := chatRequest(t, dataServer.URL, localKey, "upstream/"+mockopenai.SmallModel); status != http.StatusOK || body == "" {
		t.Fatalf("restored catalog did not accept old key: status=%d body=%s", status, body)
	}
}
