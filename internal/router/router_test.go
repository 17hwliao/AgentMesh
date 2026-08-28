package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentmesh/internal/provider"
)

func TestFallbackOnlyBeforeFirstChunk(t *testing.T) {
	primary := provider.NewMock(provider.MockConfig{Name: "primary", FailBeforeFirst: true, FailAfterChunks: -1})
	fallback := provider.NewMock(provider.MockConfig{Name: "fallback", Chunks: []string{"fallback output"}, FailAfterChunks: -1})
	var received []string
	err := New(primary, fallback).Stream(context.Background(), request(), func(chunk provider.Chunk) error {
		received = append(received, chunk.Delta)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if primary.Calls() != 1 || fallback.Calls() != 1 || strings.Join(received, "") != "fallback output" {
		t.Fatalf("fallback result calls=(%d,%d) chunks=%q", primary.Calls(), fallback.Calls(), received)
	}
}

func TestNoFallbackAfterFirstChunk(t *testing.T) {
	primary := provider.NewMock(provider.MockConfig{Name: "primary", Chunks: []string{"first"}, FailAfterChunks: 1})
	fallback := provider.NewMock(provider.MockConfig{Name: "fallback", Chunks: []string{"must not run"}, FailAfterChunks: -1})
	err := New(primary, fallback).Stream(context.Background(), request(), func(provider.Chunk) error { return nil })
	if !errors.Is(err, ErrStreamInterrupted) {
		t.Fatalf("Stream() error = %v, want ErrStreamInterrupted", err)
	}
	if fallback.Calls() != 0 {
		t.Fatalf("fallback calls = %d, want 0 after first chunk", fallback.Calls())
	}
}

func TestCancellationReachesMockAndReturns(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	slow := provider.NewMock(provider.MockConfig{Name: "slow", Delay: time.Second, Chunks: []string{"late"}, FailAfterChunks: -1, Started: started, Cancelled: cancelled})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(slow).Stream(ctx, request(), func(provider.Chunk) error { return nil }) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("mock attempt did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("mock did not observe request context cancellation")
	}
}

func TestObserverRecordsFallbackWithoutChangingRoute(t *testing.T) {
	primary := provider.NewMock(provider.MockConfig{Name: "primary", FailBeforeFirst: true, FailAfterChunks: -1})
	fallback := provider.NewMock(provider.MockConfig{Name: "fallback", Chunks: []string{"ok"}, FailAfterChunks: -1})
	observer := &collectingObserver{}
	if err := NewWithObserver([]provider.Provider{primary, fallback}, observer).Stream(context.Background(), request(), func(provider.Chunk) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if primary.Calls() != 1 || fallback.Calls() != 1 || len(observer.events) != 5 || observer.events[1].Outcome != "failed_before_first_chunk" || observer.events[4].Outcome != "succeeded" {
		t.Fatalf("calls=(%d,%d) events=%+v", primary.Calls(), fallback.Calls(), observer.events)
	}
}

func TestObserverPanicOrBlockDoesNotDelayStream(t *testing.T) {
	assertUnblocked := func(t *testing.T, observer Observer) {
		done := make(chan error, 1)
		go func(observer Observer) {
			candidate := provider.NewMock(provider.MockConfig{Name: "only", Chunks: []string{"ok"}, FailAfterChunks: -1})
			done <- NewWithObserver([]provider.Provider{candidate}, observer).Stream(context.Background(), request(), func(provider.Chunk) error { return nil })
		}(observer)
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Stream() error=%v", err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatal("observer delayed stream")
		}
	}
	assertUnblocked(t, panicObserver{})
	blocking := blockingObserver{entered: make(chan struct{}), release: make(chan struct{})}
	assertUnblocked(t, blocking)
	close(blocking.release)
}

type collectingObserver struct{ events []AttemptEvent }

func (o *collectingObserver) Observe(event AttemptEvent) { o.events = append(o.events, event) }
func (*collectingObserver) RouterObserverNonBlocking()   {}

type panicObserver struct{}

func (panicObserver) Observe(AttemptEvent) { panic("observer failure") }

type blockingObserver struct {
	entered chan struct{}
	release chan struct{}
}

func (o blockingObserver) Observe(AttemptEvent) {
	select {
	case <-o.entered:
	default:
		close(o.entered)
	}
	<-o.release
}

func request() provider.ChatRequest {
	return provider.ChatRequest{Model: "mock-model", Messages: []provider.Message{{Role: "user", Content: "hello"}}}
}
