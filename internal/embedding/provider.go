// Package embedding defines the non-streaming embedding boundary for
// AgentMesh. It is intentionally separate from the chat Provider contract.
package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrInvalidRequest = errors.New("embedding: invalid request")
	ErrUpstream       = errors.New("embedding: upstream failure")
	ErrProtocol       = errors.New("embedding: protocol failure")
	ErrUnavailable    = errors.New("embedding: provider unavailable")
)

const (
	DefaultDimension = 32
	MaxInputs        = 32
	MaxInputBytes    = 8192
	MaxDimension     = 8192
)

// Provider is deliberately batch-oriented and non-streaming. Model is the
// logical model requested by the tenant; an adapter may map it to its own
// configured upstream model.
type Provider interface {
	Name() string
	Embed(context.Context, string, []string) ([][]float64, error)
}

// MockProvider is deterministic and offline. It is suitable for local demos
// and contract tests, never a claim about semantic embedding quality.
type MockProvider struct{ dimension int }

func NewMockProvider(dimension int) (*MockProvider, error) {
	if dimension <= 0 || dimension > MaxDimension {
		return nil, fmt.Errorf("%w: mock dimension must be 1..%d", ErrInvalidRequest, MaxDimension)
	}
	return &MockProvider{dimension: dimension}, nil
}

func (p *MockProvider) Name() string { return "mock" }

func (p *MockProvider) Embed(ctx context.Context, _ string, inputs []string) ([][]float64, error) {
	result := make([][]float64, len(inputs))
	for index, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vector := make([]float64, p.dimension)
		for _, token := range strings.Fields(strings.ToLower(input)) {
			digest := sha256.Sum256([]byte(token))
			for offset := 0; offset+1 < len(digest); offset += 2 {
				bucket := int(uint16(digest[offset])<<8|uint16(digest[offset+1])) % p.dimension
				sign := 1.0
				if digest[(offset+2)%len(digest)]&1 == 1 {
					sign = -1
				}
				vector[bucket] += sign
			}
		}
		normalize(vector)
		result[index] = vector
	}
	return result, nil
}

func normalize(vector []float64) {
	var norm float64
	for _, value := range vector {
		norm += value * value
	}
	if norm == 0 {
		return
	}
	norm = math.Sqrt(norm)
	for index := range vector {
		vector[index] /= norm
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
