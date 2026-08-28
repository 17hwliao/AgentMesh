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

// Router tries providers in order. It can retry only before the first emitted
// chunk; this protects clients from a stream that silently changes model.
type Router struct {
	providers []provider.Provider
}

func New(providers ...provider.Provider) Router {
	return Router{providers: append([]provider.Provider(nil), providers...)}
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
		err := candidate.Stream(ctx, request, func(chunk provider.Chunk) error {
			emitted = true
			return emit(chunk)
		})
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if emitted {
			return ErrStreamInterrupted
		}
	}
	return ErrProvidersUnavailable
}
