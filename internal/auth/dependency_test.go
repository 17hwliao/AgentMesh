package auth

import (
	"agentmesh/internal/tenant"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type unavailableIdentity struct{ tenant.Store }

func (unavailableIdentity) AuthenticateChecked(context.Context, string, [sha256.Size]byte) (tenant.Tenant, bool, error) {
	return tenant.Tenant{}, false, errors.New("private database detail")
}
func TestDependencyFailureIsUnavailableAndNeverAuthenticates(t *testing.T) {
	called := false
	h := Authenticate(unavailableIdentity{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer public-test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || called || w.Header().Get("Retry-After") == "" {
		t.Fatal("identity dependency failure contract")
	}
}
