// Package raganswer implements a deliberately constrained answer boundary for
// callers that have already retrieved tenant-scoped RAG context. It is not a
// SQL assistant, a query planner, or a performance advisor.
package raganswer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var (
	ErrUnavailable = errors.New("rag answer: provider unavailable")
	ErrProtocol    = errors.New("rag answer: provider protocol failure")
	ErrRejected    = errors.New("rag answer: request rejected")
)

const (
	AnswersPath      = "/v1/rag/answers"
	MaxContexts      = 16
	MaxContextBytes  = 16 << 10
	MaxQuestionBytes = 4096
)

// Citation is an immutable reference to an input retrieval chunk. All five
// fields are required so a consumer can render, audit, and invalidate a claim.
type Citation struct {
	SourceURI  string `json:"source_uri"`
	DocumentID string `json:"document_id"`
	Version    string `json:"version"`
	ChunkID    string `json:"chunk_id"`
	Hash       string `json:"hash"`
}

// ContextChunk is supplied by the future RAG query layer. Content is treated
// as untrusted quoted data, never as instructions to the answer provider.
type ContextChunk struct {
	Citation
	Content string `json:"content"`
}

type Request struct {
	Model    string
	Question string
	Contexts []ContextChunk
}

// Fact is the only form of affirmative answer text. Requiring citations on
// each item avoids a separate uncited narrative channel.
type Fact struct {
	Text      string     `json:"text"`
	Citations []Citation `json:"citations"`
}

type Result struct {
	Facts        []Fact
	RefusalCode  string
	InputTokens  int
	OutputTokens int
}

// Provider must answer only from Request.Contexts. Implementations receive
// the caller's logical model; adapters may map it to a configured upstream.
type Provider interface {
	Name() string
	Answer(context.Context, Request) (Result, error)
}

// deadlineProvider permits a real upstream adapter to set an end-to-end handler
// deadline. The handler deliberately keeps a safe default for offline/test
// adapters which do not implement this optional contract.
type deadlineProvider interface {
	CallTimeout() time.Duration
}

// FakeProvider is deterministic and offline. It exists for contract tests and
// local integration, not as evidence of an LLM or semantic grounding.
type FakeProvider struct{}

// Name intentionally reuses the existing tenant-route "mock" marker. The
// tenant Store is shared by chat, embedding, and answer endpoints, so routes
// remain compatible without adding a second provider-name vocabulary.
func (FakeProvider) Name() string { return "mock" }

func (FakeProvider) Answer(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if len(request.Contexts) == 0 {
		return Result{RefusalCode: "insufficient_evidence"}, nil
	}
	first := request.Contexts[0]
	text := strings.TrimSpace(first.Content)
	if text == "" {
		return Result{RefusalCode: "insufficient_evidence"}, nil
	}
	// The fake intentionally echoes no question text and marks the output as an
	// excerpt. That makes it safe to assert citation plumbing without claiming
	// natural-language generation quality.
	return Result{Facts: []Fact{{Text: "Retrieved project evidence: " + excerpt(text), Citations: []Citation{first.Citation}}}, InputTokens: len(request.Question) / 4, OutputTokens: len(text) / 4}, nil
}

func excerpt(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 240 {
		return value[:240]
	}
	return value
}

func contentHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}
