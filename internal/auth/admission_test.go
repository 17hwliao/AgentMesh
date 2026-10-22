package auth

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"agentmesh/internal/tenant"
)

type waitingAuth struct {
	tenant.Store
	entered chan struct{}
	release chan struct{}
	t       *testing.T
}

func (s waitingAuth) Authenticate(ctx context.Context, _ string, _ [sha256.Size]byte) (tenant.Tenant, bool) {
	if _, ok := ctx.Deadline(); !ok {
		s.t.Error("database authentication has no deadline")
	}
	s.entered <- struct{}{}
	select {
	case <-s.release:
		return tenant.Tenant{ID: "t"}, true
	case <-ctx.Done():
		return tenant.Tenant{}, false
	}
}

func TestAuthenticationFloodIsBoundedAndStreamKeepsOriginalContext(t *testing.T) {
	store := waitingAuth{entered: make(chan struct{}, 8), release: make(chan struct{}), t: t}
	h := Authenticate(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, limited := r.Context().Deadline(); limited || r.Context().Err() != nil {
			t.Error("auth cleanup cancelled/limited the stream")
		}
	}))
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer test-token-for-bounded-auth")
		return r
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() { defer group.Done(); h.ServeHTTP(httptest.NewRecorder(), request()) }()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-store.entered:
		case <-time.After(time.Second):
			t.Fatal("admission did not reach its documented capacity")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request())
	if w.Code != 429 {
		t.Fatalf("overflow status=%d", w.Code)
	}
	close(store.release)
	group.Wait()
}
