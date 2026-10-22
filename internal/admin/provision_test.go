package admin

import (
	"agentmesh/internal/tenant"
	"context"
	"crypto/sha256"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

type existingTenant struct{ fakeLifecycle }

func (existingTenant) CreateTenant(context.Context, tenant.Tenant) error {
	return tenant.ErrTenantExists
}
func TestProvisionRetryFailureIsServiceFailureNotIdentityConflict(t *testing.T) {
	h := NewHandler(&existingTenant{}, sha256.Sum256([]byte("admin-token")), allowMock)
	h.SetQuota(func(context.Context, string) error { return errors.New("offline") }, nil)
	r := httptest.NewRequest("POST", "/admin/tenants", strings.NewReader(`{"tenant_id":"user","model_routes":{"m":["mock"]}}`))
	r.Header.Set("Authorization", "Bearer admin-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "quota_provision_failed") {
		t.Fatal(w.Code, w.Body.String())
	}
}
