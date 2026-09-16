// Package ratelimit provides per-tenant request limiting backed by either a
// process-local bucket or an atomic Redis Lua bucket.
package ratelimit

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	EnvironmentPerMinute = "AGENTMESH_RATE_LIMIT_PER_MINUTE"
	EnvironmentBurst     = "AGENTMESH_RATE_LIMIT_BURST"
	EnvironmentStore     = "AGENTMESH_RATE_LIMIT_STORE"
	EnvironmentRedisURL  = "AGENTMESH_RATE_LIMIT_REDIS_URL"
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
	Admit(context.Context, string) Decision
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
func (g *TokenBucket) Admit(_ context.Context, tenantID string) Decision {
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

// Runtime owns optional external limiter resources. A nil Gate means limiting
// is intentionally disabled.
type Runtime struct {
	Gate  Gate
	Close func() error
}

// OpenConfiguredRuntime selects a local or Redis-backed limiter. Existing
// configurations retain the process-local behavior unless STORE=redis is set.
// Redis configuration is fail-closed: invalid or unreachable Redis prevents
// API startup instead of silently creating per-process buckets.
func OpenConfiguredRuntime(lookup func(string) string) (Runtime, error) {
	if lookup == nil {
		return Runtime{}, &ConfigurationError{Code: CodeConfiguration}
	}
	store := strings.TrimSpace(lookup(EnvironmentStore))
	if store == "" || store == "memory" {
		gate, err := OpenConfigured(lookup)
		return Runtime{Gate: gate, Close: func() error { return nil }}, err
	}
	if store != "redis" {
		return Runtime{}, &ConfigurationError{Code: CodeConfiguration}
	}
	rate, rateOK := positiveInteger(strings.TrimSpace(lookup(EnvironmentPerMinute)))
	burst, burstOK := positiveInteger(strings.TrimSpace(lookup(EnvironmentBurst)))
	url := strings.TrimSpace(lookup(EnvironmentRedisURL))
	if !rateOK || !burstOK || url == "" {
		return Runtime{}, &ConfigurationError{Code: CodeConfiguration}
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		return Runtime{}, &ConfigurationError{Code: CodeConfiguration}
	}
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return Runtime{}, &ConfigurationError{Code: CodeConfiguration}
	}
	gate, err := NewRedis(Config{PerMinute: rate, Burst: burst}, client)
	if err != nil {
		_ = client.Close()
		return Runtime{}, err
	}
	return Runtime{Gate: gate, Close: client.Close}, nil
}

const redisBucketPrefix = "agentmesh:rate_limit:v1:"

const redisAdmitLua = `
local current = redis.call('HMGET', KEYS[1], 'tokens', 'updated')
local clock = redis.call('TIME')
local now = tonumber(clock[1]) + tonumber(clock[2]) / 1000000
local burst = tonumber(ARGV[1])
local per_second = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])
local tokens = tonumber(current[1])
local updated = tonumber(current[2])
if tokens == nil or updated == nil then
  tokens = burst
  updated = now
end
local elapsed = now - updated
if elapsed < 0 then elapsed = 0 end
tokens = math.min(burst, tokens + elapsed * per_second)
local allowed = 0
local retry_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry_ms = math.ceil(((1 - tokens) / per_second) * 1000)
end
redis.call('HMSET', KEYS[1], 'tokens', tokens, 'updated', now)
redis.call('EXPIRE', KEYS[1], ttl)
return {allowed, retry_ms}
`

type redisRateEvaluator interface {
	Eval(context.Context, string, []string, ...any) (any, error)
}

type goRedisRateEvaluator struct{ client redis.Scripter }

func (e goRedisRateEvaluator) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return e.client.Eval(ctx, script, keys, args...).Result()
}

// RedisTokenBucket shares a per-tenant bucket across API processes. Redis TIME
// rather than the gateway clock is used so replicas cannot mint tokens due to
// clock skew.
type RedisTokenBucket struct {
	evaluator redisRateEvaluator
	config    Config
}

func NewRedis(config Config, client redis.Scripter) (*RedisTokenBucket, error) {
	return newRedis(config, goRedisRateEvaluator{client: client})
}

func newRedis(config Config, evaluator redisRateEvaluator) (*RedisTokenBucket, error) {
	if config.PerMinute <= 0 || config.Burst <= 0 || evaluator == nil {
		return nil, &ConfigurationError{Code: CodeConfiguration}
	}
	if config.IdleTTL == 0 {
		config.IdleTTL = defaultIdleTTL
	}
	if config.IdleTTL <= 0 {
		return nil, &ConfigurationError{Code: CodeConfiguration}
	}
	return &RedisTokenBucket{evaluator: evaluator, config: config}, nil
}

func (g *RedisTokenBucket) Admit(ctx context.Context, tenantID string) Decision {
	if g == nil || g.evaluator == nil || tenantID == "" || ctx == nil {
		return Decision{RetryAfter: time.Second}
	}
	result, err := g.evaluator.Eval(ctx, redisAdmitLua, []string{redisBucketPrefix + tenantID}, g.config.Burst, float64(g.config.PerMinute)/60, int(g.config.IdleTTL.Seconds()))
	if err != nil {
		return Decision{RetryAfter: time.Second}
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return Decision{RetryAfter: time.Second}
	}
	allowed, allowedOK := asInt64(values[0])
	retryMS, retryOK := asInt64(values[1])
	if !allowedOK || !retryOK || retryMS < 0 {
		return Decision{RetryAfter: time.Second}
	}
	if allowed == 1 {
		return Decision{Allowed: true}
	}
	if allowed != 0 {
		return Decision{RetryAfter: time.Second}
	}
	return Decision{RetryAfter: time.Duration(retryMS) * time.Millisecond}
}

func asInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case int64:
		return number, true
	case int:
		return int64(number), true
	case uint64:
		if number <= uint64(^uint64(0)>>1) {
			return int64(number), true
		}
	}
	return 0, false
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
