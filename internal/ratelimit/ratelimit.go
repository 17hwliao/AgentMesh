// Package ratelimit provides a process-local, per-tenant request limiter.
package ratelimit

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	EnvironmentPerMinute = "AGENTMESH_RATE_LIMIT_PER_MINUTE"
	EnvironmentBurst     = "AGENTMESH_RATE_LIMIT_BURST"
	CodeConfiguration    = "rate_limit_configuration_invalid"
	defaultIdleTTL       = 15 * time.Minute
	defaultMaxBuckets    = 10_000
)

// ConfigurationError contains a stable startup rejection code only.
type ConfigurationError struct{ Code string }

func (e *ConfigurationError) Error() string { return e.Code }

// Config applies equally to each tenant bucket in the current process.
type Config struct {
	PerMinute  int
	Burst      int
	IdleTTL    time.Duration
	MaxBuckets int
}

// Decision reports whether one request token was admitted. RetryAfter is set
// only for a denied request and represents the remaining time until one token.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// Gate is the narrow boundary consumed by the HTTP gateway.
type Gate interface {
	Admit(tenantID string) Decision
}

type bucket struct {
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

// TokenBucket is safe for concurrent requests. It deliberately owns no
// persistence or cross-process state.
type TokenBucket struct {
	mu      sync.Mutex
	config  Config
	now     func() time.Time
	buckets map[string]bucket
}

// OpenConfigured leaves limiting disabled only when both variables are absent.
func OpenConfigured(lookup func(string) string) (Gate, error) {
	if lookup == nil {
		return nil, &ConfigurationError{Code: CodeConfiguration}
	}
	perMinute := strings.TrimSpace(lookup(EnvironmentPerMinute))
	burst := strings.TrimSpace(lookup(EnvironmentBurst))
	if perMinute == "" && burst == "" {
		return nil, nil
	}
	rate, rateOK := positiveInteger(perMinute)
	capacity, capacityOK := positiveInteger(burst)
	if !rateOK || !capacityOK {
		return nil, &ConfigurationError{Code: CodeConfiguration}
	}
	return New(Config{PerMinute: rate, Burst: capacity}, nil)
}

// New constructs a limiter with a controllable clock for deterministic tests.
func New(config Config, now func() time.Time) (*TokenBucket, error) {
	if config.PerMinute <= 0 || config.Burst <= 0 {
		return nil, &ConfigurationError{Code: CodeConfiguration}
	}
	if config.IdleTTL == 0 {
		config.IdleTTL = defaultIdleTTL
	}
	if config.MaxBuckets == 0 {
		config.MaxBuckets = defaultMaxBuckets
	}
	if config.IdleTTL <= 0 || config.MaxBuckets <= 0 {
		return nil, &ConfigurationError{Code: CodeConfiguration}
	}
	if now == nil {
		now = time.Now
	}
	return &TokenBucket{config: config, now: now, buckets: make(map[string]bucket)}, nil
}

// Admit consumes one token only when the tenant bucket has one available.
func (g *TokenBucket) Admit(tenantID string) Decision {
	if g == nil || tenantID == "" {
		return Decision{Allowed: false, RetryAfter: time.Second}
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.now()
	g.evictIdle(now)
	current, found := g.buckets[tenantID]
	if !found {
		if len(g.buckets) >= g.config.MaxBuckets {
			return Decision{RetryAfter: time.Second}
		}
		current = bucket{tokens: float64(g.config.Burst), updated: now, lastSeen: now}
	}
	if elapsed := now.Sub(current.updated); elapsed > 0 {
		current.tokens += elapsed.Seconds() * float64(g.config.PerMinute) / 60
		if current.tokens > float64(g.config.Burst) {
			current.tokens = float64(g.config.Burst)
		}
		current.updated = now
	}
	current.lastSeen = now
	if current.tokens >= 1 {
		current.tokens--
		g.buckets[tenantID] = current
		return Decision{Allowed: true}
	}
	remaining := (1 - current.tokens) * 60 / float64(g.config.PerMinute)
	g.buckets[tenantID] = current
	return Decision{RetryAfter: time.Duration(remaining * float64(time.Second))}
}

func (g *TokenBucket) evictIdle(now time.Time) {
	for tenantID, current := range g.buckets {
		if !now.Before(current.lastSeen) && now.Sub(current.lastSeen) >= g.config.IdleTTL {
			delete(g.buckets, tenantID)
		}
	}
}

func positiveInteger(value string) (int, bool) {
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil && parsed > 0
}

// IsConfigurationError exposes only the stable configuration code.
func IsConfigurationError(err error) (string, bool) {
	var configuration *ConfigurationError
	if errors.As(err, &configuration) {
		return configuration.Code, true
	}
	return "", false
}
