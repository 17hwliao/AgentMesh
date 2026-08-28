// Package provider defines the small boundary between AgentMesh routing and a
// model implementation. The first vertical slice intentionally supports only
// streaming because the public endpoint is stream-only.
package provider

import "context"

// Message is an OpenAI-style chat message accepted by the local gateway.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is input to one provider attempt. It contains no credentials or
// tenant state in this mock-only stage.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// Usage is optional provider-observed usage. Billing semantics are deliberately
// out of scope until the quota/reservation stage.
type Usage struct {
	InputTokens  int  `json:"input_tokens"`
	OutputTokens int  `json:"output_tokens"`
	Estimated    bool `json:"estimated"`
}

// Chunk is one incremental stream result.
type Chunk struct {
	Delta string `json:"delta"`
	Usage *Usage `json:"usage,omitempty"`
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
