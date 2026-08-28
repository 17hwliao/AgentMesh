// Package gateway exposes the local HTTP/SSE boundary for AgentMesh.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
	"agentmesh/internal/router"
)

const (
	chatPath        = "/v1/chat/completions"
	healthPath      = "/healthz"
	maxRequestBytes = 64 << 10
	modelNotAllowed = "model_not_allowed"
	quotaExhausted  = "quota_exhausted"
)

// TenantResolver supplies only the already-authorized, already-constructed
// adapters for an authenticated tenant and model.
type TenantResolver interface {
	Streamer(tenantID, model string) (router.Streamer, bool)
	VisibleProviders(tenantID string) []provider.Provider
}

// QuotaGate is deliberately allow-only in this slice. Redis reservations and
// charging are a later feature, but the pre-provider rejection boundary is
// explicit and testable now.
type QuotaGate interface {
	Allow(context.Context, string, string) bool
}

type allowQuotaGate struct{}

func (allowQuotaGate) Allow(context.Context, string, string) bool { return true }

// Server is intentionally local-only in this first mock gateway stage.
type Server struct {
	router         router.Streamer
	providers      []provider.Provider
	tenantResolver TenantResolver
	quota          QuotaGate
}

func New(streamer router.Streamer) *Server { return NewWithHealth(streamer) }

func NewWithHealth(streamer router.Streamer, providers ...provider.Provider) *Server {
	return &Server{router: streamer, providers: append([]provider.Provider(nil), providers...), quota: allowQuotaGate{}}
}

// NewWithTenantRouting keeps route declarations separate from construction:
// Resolver receives the actual adapters built by runtime.Build.
func NewWithTenantRouting(resolver TenantResolver, quota ...QuotaGate) *Server {
	gate := QuotaGate(allowQuotaGate{})
	if len(quota) == 1 && quota[0] != nil {
		gate = quota[0]
	}
	return &Server{tenantResolver: resolver, quota: gate}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(healthPath, s.handleHealth)
	mux.HandleFunc("/health/providers", s.handleProviderHealth)
	mux.HandleFunc(chatPath, s.handleChat)
	return mux
}

// AuthenticatedHandler leaves liveness public but places authentication before
// body decoding, tenant routing, health checks, and provider attempts.
func (s *Server) AuthenticatedHandler(authenticate func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(healthPath, s.handleHealth)
	mux.Handle("/health/providers", authenticate(http.HandlerFunc(s.handleProviderHealth)))
	mux.Handle(chatPath, authenticate(http.HandlerFunc(s.handleChat)))
	return mux
}

func (s *Server) handleProviderHealth(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	type status struct {
		Name    string `json:"name"`
		Healthy bool   `json:"healthy"`
	}
	providers := s.providers
	if s.tenantResolver != nil {
		tenantID, ok := auth.TenantID(request.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, auth.CodeFailed)
			return
		}
		providers = s.tenantResolver.VisibleProviders(tenantID)
	}
	result := make([]status, 0, len(providers))
	for _, candidate := range providers {
		result = append(result, status{Name: candidate.Name(), Healthy: candidate.Health(request.Context()) == nil})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"providers": result})
}

// ValidateListenAddress rejects wildcard, LAN, and IPv6 bindings. The mock
// gateway has no authentication, so it must not be reachable beyond localhost.
func ValidateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" {
		return errors.New("listen address must be 127.0.0.1:PORT")
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

type chatRequest struct {
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
	Stream   bool               `json:"stream"`
}

func (s *Server) handleChat(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	input, err := decodeChatRequest(request)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_chat_request")
		return
	}

	streamer := s.router
	if s.tenantResolver != nil {
		tenantID, ok := auth.TenantID(request.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, auth.CodeFailed)
			return
		}
		var allowed bool
		streamer, allowed = s.tenantResolver.Streamer(tenantID, input.Model)
		if !allowed {
			writeJSONError(w, http.StatusForbidden, modelNotAllowed)
			return
		}
		if !s.quota.Allow(request.Context(), tenantID, input.Model) {
			writeJSONError(w, http.StatusTooManyRequests, quotaExhausted)
			return
		}
	}
	if streamer == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "provider_unavailable")
		return
	}
	started := false
	start := func() {
		if started {
			return
		}
		started = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	}
	err = streamer.Stream(request.Context(), provider.ChatRequest{Model: input.Model, Messages: input.Messages}, func(chunk provider.Chunk) error {
		start()
		return writeSSE(w, map[string]any{
			"choices": []map[string]any{{"delta": map[string]string{"content": chunk.Delta}}},
			"usage":   chunk.Usage,
		})
	})
	if err == nil {
		if !started {
			start()
		}
		_ = writeDone(w)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if started {
		_ = writeSSE(w, map[string]any{"error": map[string]string{"code": streamErrorCode(err)}})
		_ = writeDone(w)
		return
	}
	writeJSONError(w, http.StatusServiceUnavailable, streamErrorCode(err))
}

func decodeChatRequest(request *http.Request) (chatRequest, error) {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	var input chatRequest
	if err := decoder.Decode(&input); err != nil {
		return chatRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return chatRequest{}, errors.New("trailing JSON")
	}
	if strings.TrimSpace(input.Model) == "" || len(input.Messages) == 0 || !input.Stream {
		return chatRequest{}, errors.New("missing required chat fields")
	}
	for _, message := range input.Messages {
		if strings.TrimSpace(message.Role) == "" {
			return chatRequest{}, errors.New("message role is required")
		}
	}
	return input, nil
}

func streamErrorCode(err error) string {
	if errors.Is(err, router.ErrStreamInterrupted) {
		return "stream_interrupted"
	}
	return "provider_unavailable"
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}

func writeSSE(w http.ResponseWriter, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeDone(w http.ResponseWriter) error {
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
