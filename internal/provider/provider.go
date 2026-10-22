// Package provider defines the small boundary between AgentMesh routing and a
// model implementation. Adapters stream internally; HTTP clients may request
// streaming or a bounded aggregated response.
package provider

import (
	"context"
	"encoding/json"
)

// Message is an OpenAI-style chat message accepted by the local gateway.
type Message struct {
	Role       string        `json:"role"`
	Content    string        `json:"-"`
	Parts      []ContentPart `json:"-"`
	Name       string        `json:"-"`
	ToolCallID string        `json:"-"`
	ToolCalls  []ToolCall    `json:"-"`
}

// ChatRequest is input to one provider attempt. It contains no credentials or
// tenant state. Identity and upstream credentials remain server-owned.
type ChatRequest struct {
	Model             string           `json:"model"`
	Messages          []Message        `json:"messages"`
	MaxTokens         int              `json:"max_tokens,omitempty"`
	Temperature       *float64         `json:"temperature,omitempty"`
	Tools             []ToolDefinition `json:"tools,omitempty"`
	ToolChoice        json.RawMessage  `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	TopP              *float64         `json:"top_p,omitempty"`
	Stop              json.RawMessage  `json:"stop,omitempty"`
	ResponseFormat    json.RawMessage  `json:"response_format,omitempty"`
	Seed              *int             `json:"seed,omitempty"`
}

// Usage is provider-observed usage; only verified actual usage settles money.
type Usage struct {
	InputTokens       int  `json:"input_tokens"`
	OutputTokens      int  `json:"output_tokens"`
	Estimated         bool `json:"estimated"`
	CachedInputTokens int  `json:"cached_input_tokens,omitempty"`
}

// Chunk is one incremental stream result.
type Chunk struct {
	Delta        string          `json:"delta"`
	Usage        *Usage          `json:"usage,omitempty"`
	ToolCalls    []ToolCallDelta `json:"tool_calls,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
}

// Emit forwards one chunk. Returning an error stops the provider attempt.
type Emit func(Chunk) error

// Provider is implemented by a model adapter. Implementations must observe
// ctx and return after it is cancelled.
type Provider interface {
	Name() string
	Health(context.Context) error
	Stream(context.Context, ChatRequest, Emit) error
}
