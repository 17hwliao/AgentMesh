package embedding

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/tenant"
)

const EmbeddingsPath = "/v1/embeddings"

type Handler struct {
	store     tenant.Store
	providers map[string]Provider
	now       func() time.Time
}

func NewHandler(store tenant.Store, providers []Provider) (*Handler, error) {
	if store == nil || len(providers) == 0 {
		return nil, errors.New("embedding handler requires tenant store and provider")
	}
	byName := make(map[string]Provider, len(providers))
	for _, provider := range providers {
		if provider == nil || strings.TrimSpace(provider.Name()) == "" {
			return nil, errors.New("embedding provider is invalid")
		}
		if _, exists := byName[provider.Name()]; exists {
			return nil, fmt.Errorf("duplicate embedding provider %q", provider.Name())
		}
		byName[provider.Name()] = provider
	}
	return &Handler{store: store, providers: byName, now: func() time.Time { return time.Now().UTC() }}, nil
}

type request struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

type response struct {
	Object     string          `json:"object"`
	Model      string          `json:"model"`
	Data       []embeddingData `json:"data"`
	TraceID    string          `json:"trace_id"`
	Provider   string          `json:"provider"`
	InputCount int             `json:"input_count"`
	Dimension  int             `json:"dimension"`
	DurationMS int64           `json:"duration_ms"`
}

type embeddingData struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	tenantID, ok := auth.TenantID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, auth.CodeFailed)
		return
	}
	input, err := decodeRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_embedding_request")
		return
	}
	route, ok := h.store.Route(r.Context(), tenantID, input.Model)
	if !ok {
		writeError(w, http.StatusForbidden, "model_not_allowed")
		return
	}
	provider, ok := firstProvider(route, h.providers)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "embedding_provider_unavailable")
		return
	}
	traceID := newTraceID()
	w.Header().Set("X-AgentMesh-Trace-ID", traceID)
	started := h.now()
	vectors, err := provider.Embed(r.Context(), input.Model, input.Inputs)
	if err != nil {
		writeError(w, providerStatus(err), providerCode(err))
		return
	}
	if err := validateVectorsForRequest(vectors, len(input.Inputs)); err != nil {
		writeError(w, http.StatusBadGateway, "embedding_provider_protocol_error")
		return
	}
	data := make([]embeddingData, len(vectors))
	for index, vector := range vectors {
		data[index] = embeddingData{Object: "embedding", Embedding: vector, Index: index}
	}
	result := response{Object: "list", Model: input.Model, Data: data, TraceID: traceID, Provider: provider.Name(), InputCount: len(input.Inputs), Dimension: len(vectors[0]), DurationMS: h.now().Sub(started).Milliseconds()}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

type decodedRequest struct {
	Model  string
	Inputs []string
}

func decodeRequest(r *http.Request) (decodedRequest, error) {
	if r.ContentLength > 64<<10 {
		return decodedRequest{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10+1))
	decoder.DisallowUnknownFields()
	var input request
	if err := decoder.Decode(&input); err != nil {
		return decodedRequest{}, ErrInvalidRequest
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return decodedRequest{}, ErrInvalidRequest
	}
	if strings.TrimSpace(input.Model) == "" || len(input.Input) == 0 {
		return decodedRequest{}, ErrInvalidRequest
	}
	rawInput := bytes.TrimSpace(input.Input)
	var inputs []string
	if len(rawInput) == 0 {
		return decodedRequest{}, ErrInvalidRequest
	}
	if rawInput[0] == '"' {
		var single string
		if err := json.Unmarshal(rawInput, &single); err != nil {
			return decodedRequest{}, ErrInvalidRequest
		}
		inputs = []string{single}
	} else if rawInput[0] == '[' {
		if err := json.Unmarshal(rawInput, &inputs); err != nil {
			return decodedRequest{}, ErrInvalidRequest
		}
	} else {
		return decodedRequest{}, ErrInvalidRequest
	}
	if len(inputs) == 0 || len(inputs) > MaxInputs {
		return decodedRequest{}, ErrInvalidRequest
	}
	for _, value := range inputs {
		if strings.TrimSpace(value) == "" || len([]byte(value)) > MaxInputBytes {
			return decodedRequest{}, ErrInvalidRequest
		}
	}
	return decodedRequest{Model: input.Model, Inputs: inputs}, nil
}

func validateVectorsForRequest(vectors [][]float64, count int) error {
	if len(vectors) != count || len(vectors) == 0 || len(vectors[0]) == 0 || len(vectors[0]) > MaxDimension {
		return ErrProtocol
	}
	dimension := len(vectors[0])
	for _, vector := range vectors {
		if len(vector) != dimension {
			return ErrProtocol
		}
	}
	return nil
}

func firstProvider(route []string, providers map[string]Provider) (Provider, bool) {
	for _, name := range route {
		if provider, ok := providers[name]; ok {
			return provider, true
		}
	}
	return nil, false
}

func providerStatus(err error) int {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrProtocol):
		return http.StatusBadGateway
	default:
		return http.StatusServiceUnavailable
	}
}

func providerCode(err error) string {
	switch {
	case errors.Is(err, ErrProtocol):
		return "embedding_provider_protocol_error"
	default:
		return "embedding_provider_unavailable"
	}
}

func newTraceID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "embedding-trace-unavailable"
	}
	return hex.EncodeToString(bytes)
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}
