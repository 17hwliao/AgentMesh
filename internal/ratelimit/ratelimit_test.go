package ratelimit

import (
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
	if !gate.Admit("tenant-a").Allowed || !gate.Admit("tenant-a").Allowed {
		t.Fatal("initial burst was not admitted")
	}
	denied := gate.Admit("tenant-a")
	if denied.Allowed || denied.RetryAfter != time.Minute {
		t.Fatalf("denied=%+v", denied)
	}
	if !gate.Admit("tenant-b").Allowed {
		t.Fatal("tenant-b shared tenant-a bucket")
	}
	now = now.Add(30 * time.Second)
	denied = gate.Admit("tenant-a")
	if denied.Allowed || denied.RetryAfter < 29*time.Second || denied.RetryAfter > 30*time.Second {
		t.Fatalf("partial refill=%+v", denied)
	}
	now = now.Add(30 * time.Second)
	if !gate.Admit("tenant-a").Allowed {
		t.Fatal("full refill was not admitted")
	}
}

func TestTokenBucketDoesNotMintTokensWhenClockMovesBackward(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	gate, err := New(Config{PerMinute: 1, Burst: 1}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	_ = gate.Admit("tenant-a")
	now = now.Add(-time.Hour)
	if decision := gate.Admit("tenant-a"); decision.Allowed {
		t.Fatalf("clock rollback admitted=%+v", decision)
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
			allowed <- gate.Admit("tenant-a").Allowed
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
