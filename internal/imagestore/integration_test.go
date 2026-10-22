package imagestore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentmesh/internal/admin"
	"agentmesh/internal/auth"
	"agentmesh/internal/gateway"
	"agentmesh/internal/imagestore"
	"agentmesh/internal/provider"
	"agentmesh/internal/tenant"
	"agentmesh/migrations"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if e := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

type visionFixture struct{ calls atomic.Int32 }

func (p *visionFixture) Name() string                 { return "mock" }
func (p *visionFixture) Health(context.Context) error { return nil }
func (p *visionFixture) SupportsImages(string) bool   { return true }
func (p *visionFixture) Stream(_ context.Context, r provider.ChatRequest, emit provider.Emit) error {
	if len(r.Messages) != 1 || len(r.Messages[0].Parts) != 1 || !strings.HasPrefix(r.Messages[0].Parts[0].ImageURL.URL, "data:image/png;base64,") {
		return provider.ErrProtocol
	}
	p.calls.Add(1)
	return emit(provider.Chunk{Delta: "private image received", Usage: &provider.Usage{InputTokens: 100, OutputTokens: 3}})
}
func TestUploadValidationRejectsTruncatedAndMismatchedImages(t *testing.T) {
	raw := pngBytes(t)
	if imagestore.Validate(raw, "image/png") != nil {
		t.Fatal("valid PNG rejected")
	}
	for _, c := range []struct {
		raw   []byte
		media string
	}{{raw[:33], "image/png"}, {raw, "image/jpeg"}, {[]byte("<svg>"), "image/svg+xml"}, {bytes.Repeat([]byte{'x'}, imagestore.MaxBytes+1), "image/png"}} {
		if imagestore.Validate(c.raw, c.media) == nil {
			t.Fatal("invalid upload accepted")
		}
	}
}
func TestRealMySQLAccountLifecycleAndPrivateImages(t *testing.T) {
	dsn := os.Getenv("AGENTMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("explicit isolated MySQL required")
	}
	store, db, e := tenant.OpenMySQLStore(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(16)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if e = migrations.Apply(ctx, db); e != nil {
		t.Fatal(e)
	}
	life := tenant.NewMySQLLifecycle(db)
	id := fmt.Sprintf("images_%d", time.Now().UnixNano())
	other := id + "b"
	for _, owner := range []string{id, other} {
		if e = life.CreateTenant(ctx, tenant.Tenant{ID: owner, ModelRoutes: map[string][]string{"chat": {"mock"}}}); e != nil {
			t.Fatal(e)
		}
	}
	_, key, e := life.CreateAPIKey(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	_, otherKey, e := life.CreateAPIKey(ctx, other)
	if e != nil {
		t.Fatal(e)
	}
	images := &imagestore.Store{DB: db}
	vision := &visionFixture{}
	resolver, e := tenant.NewResolver(ctx, store, []string{"mock"}, []provider.Provider{vision})
	if e != nil {
		t.Fatal(e)
	}
	gatewayServer := gateway.NewWithTenantRouting(resolver)
	gatewayServer.SetImageResolver(images.Resolve)
	chat := gatewayServer.AuthenticatedHandler(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) })
	handler := images.Handler()
	mux := http.NewServeMux()
	mux.Handle("/v1/images", auth.Authenticate(store, handler))
	mux.Handle("/v1/images/", auth.Authenticate(store, handler))
	mux.Handle("/v1/account", auth.Authenticate(store, admin.AccountHandler(life)))
	manager := admin.NewHandler(life, sha256.Sum256([]byte("private-administrator")), func(route []string) bool { return len(route) == 1 && route[0] == "mock" })
	mux.Handle("/admin/", manager)
	call := func(method, path, key, media string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		r.Header.Set("Content-Type", media)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	raw := pngBytes(t)
	if w := call("POST", "/v1/images", "", "image/png", raw); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := call("POST", "/v1/images", key, "image/png", raw)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved imagestore.Image
	if json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.ID == "" || saved.Reference == "" {
		t.Fatal(w.Body.String())
	}
	for _, clientKey := range []string{otherKey, key} {
		body := `{"model":"chat","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + saved.Reference + `"}}]}]}`
		request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+clientKey)
		out := httptest.NewRecorder()
		chat.ServeHTTP(out, request)
		if clientKey == key {
			if out.Code != 200 || !strings.Contains(out.Body.String(), "private image received") {
				t.Fatal("stored image Chat path failed", out.Code, out.Body.String())
			}
		} else if out.Code != 400 || vision.calls.Load() != 0 {
			t.Fatal("foreign reference reached model", out.Code)
		}
	}
	if w := call("GET", "/v1/images/"+saved.ID, otherKey, "", nil); w.Code != 404 {
		t.Fatal("cross-tenant image leak", w.Code)
	}
	if w := call("GET", "/v1/images/"+saved.ID, key, "", nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), raw) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private download corrupted")
	}
	restarted := &imagestore.Store{DB: db}
	for _, clientKey := range []string{key, otherKey} {
		w := call("GET", "/v1/images", clientKey, "", nil)
		var page struct {
			Data []imagestore.Image `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("image list: %d %s", w.Code, w.Body.String())
		}
		if clientKey == key && (len(page.Data) != 1 || page.Data[0].ID != saved.ID) {
			t.Fatal("own image missing")
		}
		if clientKey == otherKey && len(page.Data) != 0 {
			t.Fatal("foreign image listed")
		}
		if bytes.Contains(w.Body.Bytes(), []byte(`"Content"`)) {
			t.Fatal("raw content in metadata list")
		}
	}
	if w := call("GET", "/v1/images?after=invalid", key, "", nil); w.Code != 400 {
		t.Fatal("invalid image cursor accepted")
	}
	if row, e := restarted.Get(ctx, id, saved.ID); e != nil || !bytes.Equal(row.Content, raw) {
		t.Fatal("store restart lost image", e)
	}
	messages := []provider.Message{{Role: "user", Parts: []provider.ContentPart{{Type: "image_url", ImageURL: &provider.ImageURL{URL: saved.Reference}}}}}
	if e := restarted.Resolve(ctx, other, messages); e == nil {
		t.Fatal("cross-tenant reference resolved")
	}
	if e := restarted.Resolve(ctx, id, messages); e != nil || provider.ValidateMessages(messages) != nil || !strings.HasPrefix(messages[0].Parts[0].ImageURL.URL, "data:image/png;base64,") {
		t.Fatal("reference not converted before upstream", e)
	}
	if w := call("POST", "/admin/api-keys", key, "application/json", []byte(`{"tenant_id":"`+id+`"}`)); w.Code != 401 {
		t.Fatal("application key accessed admin")
	}
	w = call("GET", "/admin/tenants/"+id+"/api-keys", "private-administrator", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "key_hash") {
		t.Fatal("key listing leaked credentials", w.Code, w.Body.String())
	}
	manager.SetQuota(func(context.Context, string) error { return nil }, nil)
	if w := call("POST", "/admin/tenants", "private-administrator", "application/json", []byte(`{"tenant_id":"`+id+`","model_routes":{"chat":["mock"]}}`)); w.Code != 200 {
		t.Fatal("same-definition provisioning retry failed", w.Code, w.Body.String())
	}
	if w := call("POST", "/admin/tenants", "private-administrator", "application/json", []byte(`{"tenant_id":"`+id+`","model_routes":{"different":["mock"]}}`)); w.Code != 409 {
		t.Fatal("conflicting create accepted", w.Code, w.Body.String())
	}
	w = call("GET", "/v1/account?tenant_id="+other, key, "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) || strings.Contains(w.Body.String(), other) {
		t.Fatal("account selection not bound to auth")
	}
	if w := call("GET", "/admin/tenants?limit=101", "private-administrator", "", nil); w.Code != 400 {
		t.Fatal("unbounded listing accepted")
	}
	for _, body := range []string{`{}`, `{"model_routes":{}}`, `{"model_routes":{"x":["ark"]}}`, `{"enabled":false,"extra":1}`} {
		if w := call("PATCH", "/admin/tenants/"+id, "private-administrator", "application/json", []byte(body)); w.Code != 400 {
			t.Fatal("invalid patch accepted", body, w.Code)
		}
	}
	if w := call("PATCH", "/admin/tenants/"+id, "private-administrator", "application/json", []byte(`{"enabled":false}`)); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/v1/images/"+saved.ID, key, "", nil); w.Code != 401 {
		t.Fatal("disabled user retained access")
	}
	disabled := false
	if e := life.UpdateAccount(ctx, other, tenant.AccountPatch{Enabled: &disabled}); e != nil {
		t.Fatal(e)
	}
	mock := provider.NewMock(provider.MockConfig{Name: "mock", Chunks: []string{"ok"}, FailAfterChunks: -1})
	if _, e := tenant.NewResolver(ctx, store, []string{"mock"}, []provider.Provider{mock}); e != nil {
		t.Fatal("all-disabled deployment cannot restart", e)
	}
	enabled := true
	if e := life.UpdateAccount(ctx, id, tenant.AccountPatch{Enabled: &enabled, ModelRoutes: map[string][]string{"next": {"mock"}}}); e != nil {
		t.Fatal(e)
	}
	if _, ok := store.Route(ctx, id, "chat"); ok {
		t.Fatal("old route survived replacement")
	}
	if _, ok := store.Route(ctx, id, "next"); !ok {
		t.Fatal("new route unavailable")
	}
	if w := call("DELETE", "/v1/images/"+saved.ID, otherKey, "", nil); w.Code != 401 {
		t.Fatal("disabled other tenant authenticated")
	}
	if w := call("DELETE", "/v1/images/"+saved.ID, key, "", nil); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/v1/images/"+saved.ID, key, "", nil); w.Code != 404 {
		t.Fatal("deleted image returned")
	}
	// Fill to one free slot, then race uploads through separate connections.
	for i := 0; i < imagestore.MaxTenantImages-1; i++ {
		if _, e := images.Put(ctx, id, "image/png", raw); e != nil {
			t.Fatal(e)
		}
	}
	var wins atomic.Int32
	var failures atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := images.Put(ctx, id, "image/png", raw)
			if e == nil {
				wins.Add(1)
			} else if e != imagestore.ErrQuota {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || failures.Load() != 0 {
		t.Fatal("concurrent upload exceeded quota", wins.Load(), failures.Load())
	}
	if _, e = db.ExecContext(ctx, "UPDATE private_images SET expires_at=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND) WHERE tenant_id=?", id); e != nil {
		t.Fatal(e)
	}
	if _, e = images.Put(ctx, id, "image/png", raw); e != imagestore.ErrQuota {
		t.Fatal("expired physical bytes bypassed storage quota", e)
	}
	if e = images.PurgeExpired(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM private_images WHERE tenant_id=?", id).Scan(&count); e != nil || count != 0 {
		t.Fatal("expired data retained", e, count)
	}
	if _, e = images.Put(ctx, id, "image/png", raw); e != nil {
		t.Fatal("expired image quota was not released", e)
	}
}
