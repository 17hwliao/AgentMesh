package gateway

import (
	"agentmesh/internal/auth"
	"context"
	"net/http"
	"sync"
	"time"
)

type admission struct {
	mu        sync.Mutex
	slots     chan struct{}
	tenants   map[string]int
	perTenant int
	timeout   time.Duration
}

// SetAdmissionLimits is called before serving. Active counters are deleted
// after completion so unused tenants cannot grow a long-lived map.
func (s *Server) SetAdmissionLimits(global, perTenant int, timeout time.Duration) {
	if global < 1 || perTenant < 1 || timeout <= 0 {
		panic("invalid admission limits")
	}
	s.admission = &admission{slots: make(chan struct{}, global), tenants: map[string]int{}, perTenant: perTenant, timeout: timeout}
}

func (s *Server) withConcurrency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := s.admission
		if a == nil {
			next.ServeHTTP(w, r)
			return
		}
		tenant, _ := auth.TenantID(r.Context())
		a.mu.Lock()
		if a.tenants[tenant] >= a.perTenant {
			a.mu.Unlock()
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, 429, "concurrency_limited")
			return
		}
		select {
		case a.slots <- struct{}{}:
			a.tenants[tenant]++
			a.mu.Unlock()
		default:
			a.mu.Unlock()
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, 429, "concurrency_limited")
			return
		}
		defer func() {
			a.mu.Lock()
			a.tenants[tenant]--
			if a.tenants[tenant] == 0 {
				delete(a.tenants, tenant)
			}
			a.mu.Unlock()
			<-a.slots
		}()
		ctx, cancel := context.WithTimeout(r.Context(), a.timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
