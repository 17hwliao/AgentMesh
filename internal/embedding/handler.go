package embedding

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/observability"
	chatprovider "agentmesh/internal/provider"
	"agentmesh/internal/reservation"
	"agentmesh/internal/router"
	"agentmesh/internal/tenant"
)

const EmbeddingsPath = "/v1/embeddings"

type Handler struct {
	store     tenant.Store
	providers map[string]Provider
	now       func() time.Time
	quota     reservation.StreamGate
	recorder  *observability.Recorder
	slots     chan struct{}
}

func (h *Handler) SetResources(quota reservation.StreamGate, recorder *observability.Recorder) {
	h.quota = quota
	h.recorder = recorder
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
	return &Handler{store: store, providers: byName, now: func() time.Time { return time.Now().UTC() }, slots: make(chan struct{}, 8)}, nil
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
	Usage      *responseUsage  `json:"usage,omitempty"`
}
type responseUsage struct {
	Prompt int `json:"prompt_tokens"`
	Total  int `json:"total_tokens"`
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
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "embedding_busy")
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
	billed := false
	if marker, ok := provider.(interface{ BillableModel(string) bool }); ok {
		if !marker.BillableModel(input.Model) {
			writeError(w, 400, "invalid_billable_embedding_model")
			return
		}
		billed = true
	}
	traceID := newTraceID()
	var trace *observability.Session
	if h.recorder != nil {
		trace = h.recorder.Start(tenantID, input.Model)
		traceID = trace.TraceID()
	}
	resultCode := "embedding_provider_unavailable"
	defer func() {
		if trace != nil {
			trace.Complete(resultCode, r.Context().Err() != nil)
		}
	}()
	w.Header().Set("X-AgentMesh-Trace-ID", traceID)
	var session reservation.StreamSession
	if h.quota != nil && !billed {
		messages := make([]chatprovider.Message, len(input.Inputs))
		for i, text := range input.Inputs {
			messages[i] = chatprovider.Message{Role: "user", Content: text}
		}
		session, err = h.quota.Begin(r.Context(), tenantID, input.Model, messages)
		if err != nil {
			status := http.StatusServiceUnavailable
			code := "quota_unavailable"
			if reservation.Code(err) == "quota_exhausted" {
				status = 429
				code = "quota_exhausted"
			}
			writeError(w, status, code)
			return
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			_ = session.Complete(cleanup, resultCode, r.Context().Err() != nil)
		}()
		if err = session.BeforeAttempt(r.Context(), provider.Name(), chatprovider.ChatRequest{Model: input.Model, Messages: messages}); err != nil {
			if reservation.Code(err) == "quota_exhausted" {
				writeError(w, 429, "quota_exhausted")
			} else {
				writeError(w, 503, "quota_unavailable")
			}
			return
		}
	}
	started := h.now()
	if trace != nil {
		trace.Observe(router.AttemptEvent{Kind: router.AttemptStarted, Provider: provider.Name()})
	}
	receipt := &moneyReceipt{}
	vectors, err := provider.Embed(context.WithValue(r.Context(), receiptKey{}, receipt), input.Model, input.Inputs)
	if receipt.ID != "" {
		w.Header().Set("X-AgentMesh-Billing-Request-ID", receipt.ID)
	}
	if err != nil {
		if trace != nil {
			trace.Observe(router.AttemptEvent{Kind: router.AttemptFinished, Provider: provider.Name(), Outcome: "failed"})
		}
		if session != nil {
			_ = session.FinishAttempt(r.Context(), provider.Name(), "failed")
		}
		status, code := moneyError(err)
		if status == 0 {
			status = providerStatus(err)
			code = providerCode(err)
		}
		writeError(w, status, code)
		return
	}
	if err := validateVectorsForRequest(vectors, len(input.Inputs)); err != nil {
		if session != nil {
			_ = session.FinishAttempt(r.Context(), provider.Name(), "protocol_error")
		}
		if trace != nil {
			trace.Observe(router.AttemptEvent{Kind: router.AttemptFinished, Provider: provider.Name(), Outcome: "protocol_error"})
		}
		resultCode = "embedding_provider_protocol_error"
		writeError(w, http.StatusBadGateway, resultCode)
		return
	}
	if trace != nil {
		trace.Observe(router.AttemptEvent{Kind: router.AttemptFinished, Provider: provider.Name(), Outcome: "succeeded"})
	}
	if session != nil {
		if err := session.FinishAttempt(r.Context(), provider.Name(), "succeeded"); err != nil {
			writeError(w, 503, "quota_unavailable")
			return
		}
		if err := session.Complete(r.Context(), "success", false); err != nil {
			writeError(w, 503, "quota_unavailable")
			return
		}
	}
	data := make([]embeddingData, len(vectors))
	for index, vector := range vectors {
		data[index] = embeddingData{Object: "embedding", Embedding: vector, Index: index}
	}
	result := response{Object: "list", Model: input.Model, Data: data, TraceID: traceID, Provider: provider.Name(), InputCount: len(input.Inputs), Dimension: len(vectors[0]), DurationMS: h.now().Sub(started).Milliseconds()}
	if receipt.Usage != nil {
		result.Usage = &responseUsage{Prompt: receipt.Usage.InputTokens, Total: receipt.Usage.InputTokens}
	}
	resultCode = "success"
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *Handler) BillableModel(alias string) bool {
	for _, p := range h.providers {
		if b, ok := p.(interface{ BillableModel(string) bool }); ok && b.BillableModel(alias) {
			return true
		}
	}
	return false
}

type decodedRequest struct {
	Model  string
	Inputs []string
}

func decodeRequest(r *http.Request) (decodedRequest, error) {
	if r.ContentLength > 64<<10 {
		return decodedRequest{}, ErrInvalidRequest
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return decodedRequest{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
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
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return ErrProtocol
			}
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
