package router

import (
	"agentmesh/internal/provider"
	"context"
	"errors"
	"testing"
)

type brokenToolProvider struct{}

func (brokenToolProvider) Name() string                 { return "broken-tools" }
func (brokenToolProvider) Health(context.Context) error { return nil }
func (brokenToolProvider) SupportsTools(string) bool    { return true }
func (brokenToolProvider) Stream(_ context.Context, _ provider.ChatRequest, emit provider.Emit) error {
	if e := emit(provider.Chunk{ToolCalls: []provider.ToolCallDelta{{Index: 0, ID: "call_1", Type: "function", Function: provider.CallFunction{Name: "Read", Arguments: `{"path":`}}}}); e != nil {
		return e
	}
	return provider.ErrUpstream
}
func TestNoFallbackAfterToolArgumentsWithoutAnyText(t *testing.T) {
	fallback := provider.NewMock(provider.MockConfig{Name: "fallback", Chunks: []string{"must not run"}, FailAfterChunks: -1})
	observed := 0
	e := New(brokenToolProvider{}, fallback).Stream(context.Background(), request(), func(c provider.Chunk) error { observed += len(c.ToolCalls); return nil })
	if !errors.Is(e, ErrStreamInterrupted) || observed != 1 || fallback.Calls() != 0 {
		t.Fatal("tool execution could be duplicated by fallback", e, observed, fallback.Calls())
	}
}
