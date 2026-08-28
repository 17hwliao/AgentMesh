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
	if len(r.providers) == 0 {
		return ErrNoProvider
	}

	for _, candidate := range r.providers {
		if err := ctx.Err(); err != nil {
			return err
		}
		emitted := false
		r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptStarted})
		err := candidate.Stream(ctx, request, func(chunk provider.Chunk) error {
			if !emitted {
				r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFirstChunk})
			}
			emitted = true
			if chunk.Usage != nil {
				usage := *chunk.Usage
				r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFirstChunk, Usage: &usage})
			}
			return emit(chunk)
		})
		if err == nil {
			r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "succeeded"})
			return nil
		}
		if ctx.Err() != nil {
			r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "cancelled"})
			return ctx.Err()
		}
		if emitted {
			r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "interrupted"})
			return ErrStreamInterrupted
		}
		r.observe(AttemptEvent{Provider: candidate.Name(), Kind: AttemptFinished, Outcome: "failed_before_first_chunk"})
	}
	return ErrProvidersUnavailable
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
