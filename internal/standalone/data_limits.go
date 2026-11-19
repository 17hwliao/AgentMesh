package standalone

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ipWindow struct {
	minute time.Time
	count  int
}

type dataLimiter struct {
	mu     sync.Mutex
	ips    map[string]*ipWindow
	active int
}

func newDataLimiter() *dataLimiter {
	return &dataLimiter{ips: map[string]*ipWindow{}}
}

func (a *App) dataGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/v1" && !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if a.cfg.IPRequestsPerMinute > 0 && !a.limiter.allowIP(clientIP(r), a.cfg.IPRequestsPerMinute, time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "ip_rate_limited")
			return
		}
		if a.cfg.MaxBodyBytes > 0 {
			if r.ContentLength > a.cfg.MaxBodyBytes {
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxBodyBytes)
		}
		if a.cfg.MaxConcurrent > 0 && !a.limiter.acquire(a.cfg.MaxConcurrent) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "concurrency_limit")
			return
		}
		if a.cfg.MaxConcurrent > 0 {
			defer a.limiter.release()
		}
		next.ServeHTTP(w, r)
	})
}

func (l *dataLimiter) allowIP(ip string, limit int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	minute := now.Truncate(time.Minute)
	window := l.ips[ip]
	if window == nil || !window.minute.Equal(minute) {
		window = &ipWindow{minute: minute}
		l.ips[ip] = window
	}
	if window.count >= limit {
		return false
	}
	window.count++
	return true
}

func (l *dataLimiter) acquire(limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= limit {
		return false
	}
	l.active++
	return true
}

func (l *dataLimiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active > 0 {
		l.active--
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
