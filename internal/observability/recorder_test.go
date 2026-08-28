package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentmesh/internal/provider"
	"agentmesh/internal/router"
)

func TestRecorderStoresSafeCompletedTrace(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	recorder := NewRecorder(2, func() time.Time { return now }, sequence("trace-a"))
	session := recorder.Start("tenant_a", "model-a")
	session.Observe(router.AttemptEvent{Provider: "primary", Kind: router.AttemptStarted})
	now = now.Add(12 * time.Millisecond)
	session.Observe(router.AttemptEvent{Provider: "primary", Kind: router.AttemptFirstChunk, Usage: &provider.Usage{InputTokens: 3, OutputTokens: 5}})
	now = now.Add(8 * time.Millisecond)
	session.Observe(router.AttemptEvent{Provider: "primary", Kind: router.AttemptFinished, Outcome: "succeeded"})
	session.Complete("success", false)

	trace, ok := recorder.Get("tenant_a", "trace-a")
	if !ok || trace.Model != "model-a" || trace.FirstChunkLatencyMS == nil || *trace.FirstChunkLatencyMS != 12 || trace.TotalLatencyMS != 20 || trace.ResultCode != "success" || trace.Cancelled || !trace.UsageObserved || trace.ProviderUsage == nil || trace.ProviderUsage.OutputTokens != 5 {
		t.Fatalf("trace=%+v ok=%t", trace, ok)
	}
	if len(trace.Attempts) != 1 || trace.Attempts[0] != (Attempt{Provider: "primary", Outcome: "succeeded"}) {
		t.Fatalf("attempts=%+v", trace.Attempts)
	}
	if _, ok := recorder.Get("tenant_b", "trace-a"); ok {
		t.Fatal("cross-tenant trace was visible")
	}
}

func TestRecorderEvictsOnlyOldestCompletedAndMarksMissingUsage(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	recorder := NewRecorder(1, func() time.Time { return now }, sequence("one", "two", "three"))
	first := recorder.Start("tenant_a", "model-a")
	first.Complete("success", false)
	second := recorder.Start("tenant_a", "model-a")
	if _, ok := recorder.Get("tenant_a", "one"); ok {
		t.Fatal("oldest completed trace was not evicted")
	}
	second.Complete("success", false)
	third := recorder.Start("tenant_a", "model-a")
	if third.TraceID() != "three" {
		t.Fatalf("trace id=%q", third.TraceID())
	}
	third.Complete("success", false)
	trace, ok := recorder.Get("tenant_a", "three")
	if !ok || trace.UsageObserved || trace.ProviderUsage != nil {
		t.Fatalf("trace=%+v ok=%t", trace, ok)
	}
}

func TestRecorderDoesNotEvictPendingTrace(t *testing.T) {
	recorder := NewRecorder(1, time.Now, sequence("pending", "dropped"))
	pending := recorder.Start("tenant_a", "model-a")
	dropped := recorder.Start("tenant_a", "model-a")
	if pending.TraceID() != "pending" || dropped.TraceID() != "dropped" {
		t.Fatal("unexpected trace identifiers")
	}
	pending.Complete("success", false)
	if _, ok := recorder.Get("tenant_a", "dropped"); ok {
		t.Fatal("capacity-degraded trace became queryable")
	}
}

func TestRecorderCapturesRouterFallbackInterruptedAndCancellation(t *testing.T) {
	recorder := NewRecorder(8, time.Now, sequence("fallback", "interrupted", "cancelled"))

	fallback := recorder.Start("tenant_a", "model-a")
	primary := provider.NewMock(provider.MockConfig{Name: "primary", FailBeforeFirst: true, FailAfterChunks: -1})
	secondary := provider.NewMock(provider.MockConfig{Name: "secondary", Chunks: []string{"ok"}, FailAfterChunks: -1})
	if err := router.NewWithObserver([]provider.Provider{primary, secondary}, fallback).Stream(context.Background(), request(), func(provider.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	fallback.Complete("success", false)
	trace, ok := recorder.Get("tenant_a", "fallback")
	if !ok || len(trace.Attempts) != 2 || trace.Attempts[0].Outcome != "failed_before_first_chunk" || trace.Attempts[1].Outcome != "succeeded" {
		t.Fatalf("fallback trace=%+v ok=%t", trace, ok)
	}

	interrupted := recorder.Start("tenant_a", "model-a")
	broken := provider.NewMock(provider.MockConfig{Name: "broken", Chunks: []string{"partial"}, FailAfterChunks: 1})
	err := router.NewWithObserver([]provider.Provider{broken}, interrupted).Stream(context.Background(), request(), func(provider.Chunk) error { return nil })
	if !errors.Is(err, router.ErrStreamInterrupted) {
		t.Fatalf("interrupted error=%v", err)
	}
	interrupted.Complete("stream_interrupted", false)
	trace, ok = recorder.Get("tenant_a", "interrupted")
	if !ok || len(trace.Attempts) != 1 || trace.Attempts[0].Outcome != "interrupted" {
		t.Fatalf("interrupted trace=%+v ok=%t", trace, ok)
	}

	cancelled := recorder.Start("tenant_a", "model-a")
	started := make(chan struct{})
	slow := provider.NewMock(provider.MockConfig{Name: "slow", Delay: time.Second, Chunks: []string{"late"}, FailAfterChunks: -1, Started: started})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- router.NewWithObserver([]provider.Provider{slow}, cancelled).Stream(ctx, request(), func(provider.Chunk) error { return nil })
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error=%v", err)
	}
	cancelled.Complete("cancelled", true)
	trace, ok = recorder.Get("tenant_a", "cancelled")
	if !ok || !trace.Cancelled || len(trace.Attempts) != 1 || trace.Attempts[0].Outcome != "cancelled" {
		t.Fatalf("cancelled trace=%+v ok=%t", trace, ok)
	}
}

func sequence(values ...string) func() string {
	index := 0
	return func() string {
		if index >= len(values) {
			return ""
		}
		value := values[index]
		index++
		return value
	}
}

func request() provider.ChatRequest {
	return provider.ChatRequest{Model: "model-a", Messages: []provider.Message{{Role: "user", Content: "not stored"}}}
}
