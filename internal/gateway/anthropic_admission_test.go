package gateway

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/tenant"
)

type messagesWaitingAuth struct {
	tenant.Store
	entered chan struct{}
	release chan struct{}
}

func (s messagesWaitingAuth) Authenticate(ctx context.Context, _ string, _ [sha256.Size]byte) (tenant.Tenant, bool) {
	s.entered <- struct{}{}
	select {
	case <-s.release:
		return tenant.Tenant{ID: "fixture"}, true
	case <-ctx.Done():
		return tenant.Tenant{}, false
	}
}

func TestMessagesAuthenticationSharesAdmissionAcrossRequests(t *testing.T) {
	store := messagesWaitingAuth{entered: make(chan struct{}, 16), release: make(chan struct{})}
	handler := anthropicAuth(func(next http.Handler) http.Handler { return auth.Authenticate(store, next) }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		r.Header.Set("x-api-key", "fixture-key-for-bounded-messages-auth")
		return r
	}
	var group sync.WaitGroup
	responses := make(chan int, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, request())
			responses <- w.Code
		}()
	}
	defer func() {
		if store.release != nil {
			close(store.release)
		}
		group.Wait()
	}()
	for i := 0; i < 8; i++ {
		select {
		case <-store.entered:
		case <-time.After(time.Second):
			t.Fatal("first eight authentication requests did not enter their slots")
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request())
	if w.Code != 429 || !strings.Contains(w.Body.String(), `"type":"rate_limit_error"`) || !strings.Contains(w.Body.String(), "auth_busy") {
		t.Fatalf("ninth request must fail before database lookup: status=%d body=%s", w.Code, w.Body.String())
	}
	// Releasing the legitimate first eight must not turn them into overflow
	// failures. A later request can use their returned admission capacity.
	close(store.release)
	group.Wait()
	for i := 0; i < 8; i++ {
		if code := <-responses; code != 204 {
			t.Fatalf("admitted request was rejected: %d", code)
		}
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request())
	if w.Code != 204 {
		t.Fatalf("capacity was not released: %d", w.Code)
	}
	// Avoid a second close from the deferred cleanup on this success path.
	store.release = nil
}
