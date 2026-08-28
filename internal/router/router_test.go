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

func request() provider.ChatRequest {
	return provider.ChatRequest{Model: "mock-model", Messages: []provider.Message{{Role: "user", Content: "hello"}}}
}
