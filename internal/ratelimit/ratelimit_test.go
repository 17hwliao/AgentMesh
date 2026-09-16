package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestOpenConfiguredRequiresBothPositiveValues(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
		on   bool
	}{
		{name: "disabled", env: map[string]string{}},
		{name: "enabled", env: map[string]string{EnvironmentPerMinute: "2", EnvironmentBurst: "3"}, on: true},
		{name: "one value", env: map[string]string{EnvironmentPerMinute: "2"}},
		{name: "zero", env: map[string]string{EnvironmentPerMinute: "0", EnvironmentBurst: "3"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			gate, err := OpenConfigured(func(key string) string { return test.env[key] })
			if test.on && (err != nil || gate == nil) {
				t.Fatalf("enabled gate=%v err=%v", gate, err)
			}
			if !test.on && len(test.env) == 0 && (err != nil || gate != nil) {
				t.Fatalf("disabled gate=%v err=%v", gate, err)
			}
			if !test.on && len(test.env) > 0 {
				if code, ok := IsConfigurationError(err); !ok || code != CodeConfiguration {
					t.Fatalf("code=%q err=%v", code, err)
				}
			}
		})
	}
}

func TestTokenBucketRefillsPerTenantWithoutClockSleep(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	gate, err := New(Config{PerMinute: 1, Burst: 2}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Admit(context.Background(), "tenant-a").Allowed || !gate.Admit(context.Background(), "tenant-a").Allowed {
		t.Fatal("initial burst was not admitted")
	}
	denied := gate.Admit(context.Background(), "tenant-a")
	if denied.Allowed || denied.RetryAfter != time.Minute {
		t.Fatalf("denied=%+v", denied)
	}
	if !gate.Admit(context.Background(), "tenant-b").Allowed {
		t.Fatal("tenant-b shared tenant-a bucket")
	}
	now = now.Add(30 * time.Second)
	denied = gate.Admit(context.Background(), "tenant-a")
	if denied.Allowed || denied.RetryAfter < 29*time.Second || denied.RetryAfter > 30*time.Second {
		t.Fatalf("partial refill=%+v", denied)
	}
	now = now.Add(30 * time.Second)
	if !gate.Admit(context.Background(), "tenant-a").Allowed {
		t.Fatal("full refill was not admitted")
	}
}

func TestTokenBucketDoesNotMintTokensWhenClockMovesBackward(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	gate, err := New(Config{PerMinute: 1, Burst: 1}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_ = gate.Admit(context.Background(), "tenant-a")
	now = now.Add(-time.Hour)
	if decision := gate.Admit(context.Background(), "tenant-a"); decision.Allowed {
		t.Fatalf("clock rollback admitted=%+v", decision)
	}
}

func TestTokenBucketEvictsIdleBucketBeforeAdmittingNewTenant(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	gate, err := New(Config{PerMinute: 1, Burst: 1, IdleTTL: time.Minute, MaxBuckets: 2}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Admit(context.Background(), "tenant-idle").Allowed {
		t.Fatal("initial tenant was denied")
	}
	now = now.Add(time.Minute)
	if !gate.Admit(context.Background(), "tenant-new").Allowed {
		t.Fatal("new tenant was denied after idle eviction")
	}
	if len(gate.buckets) != 1 || gate.buckets["tenant-new"].lastSeen != now {
		t.Fatalf("buckets=%+v", gate.buckets)
	}
}

func TestTokenBucketPreservesActiveBucketsAndFailsClosedAtCapacity(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	gate, err := New(Config{PerMinute: 1, Burst: 1, IdleTTL: time.Minute, MaxBuckets: 2}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Admit(context.Background(), "tenant-a").Allowed {
		t.Fatal("tenant-a was denied")
	}
	now = now.Add(30 * time.Second)
	if !gate.Admit(context.Background(), "tenant-b").Allowed {
		t.Fatal("tenant-b was denied")
	}
	now = now.Add(20 * time.Second)
	_ = gate.Admit(context.Background(), "tenant-a") // refreshes tenant-a without making a token available.
	denied := gate.Admit(context.Background(), "tenant-c")
	if denied.Allowed || denied.RetryAfter != time.Second || len(gate.buckets) != 2 {
		t.Fatalf("decision=%+v buckets=%+v", denied, gate.buckets)
	}
	if _, ok := gate.buckets["tenant-c"]; ok {
		t.Fatal("capacity rejection created a new bucket")
	}
}

func TestTokenBucketIsConcurrent(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	gate, err := New(Config{PerMinute: 1, Burst: 20}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	allowed := make(chan bool, 100)
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			allowed <- gate.Admit(context.Background(), "tenant-a").Allowed
		}()
	}
	group.Wait()
	close(allowed)
	count := 0
	for accepted := range allowed {
		if accepted {
			count++
		}
	}
	if count != 20 {
		t.Fatalf("allowed=%d want 20", count)
	}
}

func TestRedisTokenBucketUsesOneAtomicServerTimeScript(t *testing.T) {
	evaluator := &scriptEvaluator{results: []any{[]any{int64(1), int64(0)}, []any{int64(0), int64(500)}}}
	gate, err := newRedis(Config{PerMinute: 120, Burst: 2, IdleTTL: 2 * time.Minute}, evaluator)
	if err != nil {
		t.Fatal(err)
	}
	if decision := gate.Admit(context.Background(), "tenant-a"); !decision.Allowed {
		t.Fatalf("first decision=%+v", decision)
	}
	if decision := gate.Admit(context.Background(), "tenant-a"); decision.Allowed || decision.RetryAfter != 500*time.Millisecond {
		t.Fatalf("second decision=%+v", decision)
	}
	if len(evaluator.calls) != 2 {
		t.Fatalf("calls=%d", len(evaluator.calls))
	}
	call := evaluator.calls[0]
	if call.script != redisAdmitLua || len(call.keys) != 1 || call.keys[0] != redisBucketPrefix+"tenant-a" {
		t.Fatalf("call=%+v", call)
	}
	if len(call.args) != 3 || call.args[0] != 2 || call.args[1] != 2.0 || call.args[2] != 120 {
		t.Fatalf("args=%#v", call.args)
	}
}

func TestRedisTokenBucketFailsClosedOnUnavailableOrMalformedResult(t *testing.T) {
	for _, evaluator := range []redisRateEvaluator{
		&scriptEvaluator{err: errors.New("redis unavailable")},
		&scriptEvaluator{results: []any{[]any{int64(2), int64(0)}}},
	} {
		gate, err := newRedis(Config{PerMinute: 1, Burst: 1}, evaluator)
		if err != nil {
			t.Fatal(err)
		}
		decision := gate.Admit(context.Background(), "tenant-a")
		if decision.Allowed || decision.RetryAfter != time.Second {
			t.Fatalf("decision=%+v", decision)
		}
	}
}

func TestOpenConfiguredRuntimeRejectsPartialRedisConfiguration(t *testing.T) {
	_, err := OpenConfiguredRuntime(func(key string) string {
		return map[string]string{EnvironmentStore: "redis", EnvironmentPerMinute: "1", EnvironmentBurst: "1"}[key]
	})
	if code, ok := IsConfigurationError(err); !ok || code != CodeConfiguration {
		t.Fatalf("code=%q err=%v", code, err)
	}
}

type scriptCall struct {
	script string
	keys   []string
	args   []any
}

type scriptEvaluator struct {
	results []any
	err     error
	calls   []scriptCall
}

func (e *scriptEvaluator) Eval(_ context.Context, script string, keys []string, args ...any) (any, error) {
	e.calls = append(e.calls, scriptCall{script: script, keys: append([]string(nil), keys...), args: append([]any(nil), args...)})
	if e.err != nil {
		return nil, e.err
	}
	if len(e.results) == 0 {
		return nil, errors.New("unexpected Eval")
	}
	result := e.results[0]
	e.results = e.results[1:]
	return result, nil
}
