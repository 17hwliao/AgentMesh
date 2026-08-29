// Package router chooses an ordered provider attempt without leaking provider
// implementation details into HTTP handling.
package router

import (
	"context"
	"errors"

	"agentmesh/internal/provider"
)

var (
	// ErrNoProvider means no route was configured for the requested model.
	ErrNoProvider = errors.New("no provider configured")
	// ErrProvidersUnavailable means every attempt failed before any output.
	ErrProvidersUnavailable = errors.New("providers unavailable before first chunk")
	// ErrStreamInterrupted means output was already relayed, so fallback is unsafe.
	ErrStreamInterrupted = errors.New("stream interrupted after first chunk")
)

// Streamer is the gateway's narrow routing dependency.
type Streamer interface {
	Stream(context.Context, provider.ChatRequest, provider.Emit) error
}

// AttemptHook is synchronous because it is used for durable, before-call
// reservation evidence. It is intentionally separate from Observer: an
// Observer remains best-effort diagnostics and is never allowed to block a
// route.
type AttemptHook interface {
	BeforeAttempt(context.Context, string, provider.ChatRequest) error
	BeforeEmit(context.Context, string, provider.Chunk) error
	AfterEmit(context.Context, string, provider.Chunk) error
	FinishAttempt(context.Context, string, string) error
}

// HookedStreamer is an optional extension for callers that need durable
// attempt lifecycle gates. Plain Streamer implementations remain valid for
// existing mock and client-only paths.
type HookedStreamer interface {
	Streamer
	StreamWithAttemptHook(context.Context, provider.ChatRequest, provider.Emit, AttemptHook) error
}

// AttemptEvent is a safe routing lifecycle summary. It deliberately contains
// neither request messages nor provider error text.
type AttemptEvent struct {
	Provider string
	Kind     string
	Outcome  string
	Usage    *provider.Usage
}

const (
	AttemptStarted    = "attempt_started"
	AttemptFirstChunk = "attempt_first_chunk"
	AttemptFinished   = "attempt_finished"
)

// Observer receives optional routing summaries. Observers are never allowed to
// change routing behavior: a generic observer is invoked asynchronously with
// panic recovery. Trusted in-process observers may opt into the synchronous
// marker only when their Observe implementation is non-blocking.
type Observer interface{ Observe(AttemptEvent) }
type NonBlockingObserver interface {
	Observer
	RouterObserverNonBlocking()
}

// Router tries providers in order. It can retry only before the first emitted
// chunk; this protects clients from a stream that silently changes model.
type Router struct {
	providers []provider.Provider
	observer  Observer
}

func New(providers ...provider.Provider) Router {
	return Router{providers: append([]provider.Provider(nil), providers...)}
}

func NewWithObserver(providers []provider.Provider, observer Observer) Router {
	return Router{providers: append([]provider.Provider(nil), providers...), observer: observer}
}

func (r Router) Stream(ctx context.Context, request provider.ChatRequest, emit provider.Emit) error {
	return r.StreamWithAttemptHook(ctx, request, emit, nil)
}

func (r Router) StreamWithAttemptHook(ctx context.Context, request provider.ChatRequest, emit provider.Emit, hook AttemptHook) error {
	if len(r.providers) == 0 {
		return ErrNoProvider
	}

	for _, candidate := range r.providers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hook != nil {
			if err := hook.BeforeAttempt(ctx, candidate.Name(), request); err != nil {
				return err
			}
		}
		emitted := false
		var hookFailure error
		r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptStarted})
		err := candidate.Stream(ctx, request, func(chunk provider.Chunk) error {
			if hook != nil {
				if err := hook.BeforeEmit(ctx, candidate.Name(), chunk); err != nil {
					hookFailure = hookFailureAfterProgress(ctx, hook, candidate.Name(), err)
					return hookFailure
				}
			}
			if !emitted {
				r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFirstChunk})
			}
			emitted = true
			if chunk.Usage != nil {
				usage := *chunk.Usage
				r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFirstChunk, Usage: &usage})
			}
			if err := emit(chunk); err != nil {
				return err
			}
			if hook != nil {
				if err := hook.AfterEmit(ctx, candidate.Name(), chunk); err != nil {
					hookFailure = hookFailureAfterProgress(ctx, hook, candidate.Name(), err)
					return hookFailure
				}
			}
			return nil
		})
		if hookFailure != nil {
			r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "progress_persist_failed"})
			return hookFailure
		}
		if err == nil {
			if hook != nil {
				if err := hook.FinishAttempt(ctx, candidate.Name(), "succeeded"); err != nil {
					return err
				}
			}
			r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "succeeded"})
			return nil
		}
		if ctx.Err() != nil {
			if hook != nil {
				if finishErr := hook.FinishAttempt(ctx, candidate.Name(), "cancelled"); finishErr != nil {
					return finishErr
				}
			}
			r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "cancelled"})
			return ctx.Err()
		}
		if emitted {
			if hook != nil {
				if finishErr := hook.FinishAttempt(ctx, candidate.Name(), "interrupted"); finishErr != nil {
					return finishErr
				}
			}
			r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "interrupted"})
			return ErrStreamInterrupted
		}
		if hook != nil {
			if finishErr := hook.FinishAttempt(ctx, candidate.Name(), "failed_before_first_chunk"); finishErr != nil {
				return finishErr
			}
		}
		r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "failed_before_first_chunk"})
	}
	return ErrProvidersUnavailable
}

func hookFailureAfterProgress(ctx context.Context, hook AttemptHook, providerName string, source error) error {
	if finishErr := hook.FinishAttempt(ctx, providerName, "progress_persist_failed"); finishErr != nil {
		return finishErr
	}
	return source
}

func (r Router) observe(event AttemptEvent) {
	if r.observer == nil {
		return
	}
	if _, ok := r.observer.(NonBlockingObserver); ok {
		func() {
			defer func() { _ = recover() }()
			r.observer.Observe(event)
		}()
		return
	}
	go func() {
		defer func() { _ = recover() }()
		r.observer.Observe(event)
	}()
}
