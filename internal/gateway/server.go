// Package gateway exposes the local HTTP/SSE boundary for AgentMesh.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/billing"
	"agentmesh/internal/observability"
	"agentmesh/internal/provider"
	"agentmesh/internal/ratelimit"
	"agentmesh/internal/reservation"
	"agentmesh/internal/router"
	"agentmesh/internal/semanticcache"
)

const (
	chatPath         = "/v1/chat/completions"
	healthPath       = "/healthz"
	maxRequestBytes  = provider.MaxChatBytes
	modelNotAllowed  = "model_not_allowed"
	quotaExhausted   = "quota_exhausted"
	rateLimited      = "rate_limited"
	tracePath        = "/v1/observability/traces/{trace_id}"
	traceHeader      = "X-AgentMesh-Trace-ID"
	traceNotFound    = "trace_not_found"
	quotaUnavailable = "quota_unavailable"
)

// TenantResolver supplies only the already-authorized, already-constructed
// adapters for an authenticated tenant and model.
type TenantResolver interface {
	Streamer(context.Context, string, string) (router.Streamer, bool)
	VisibleProviders(context.Context, string) []provider.Provider
}

type observedTenantResolver interface {
	TenantResolver
	StreamerWithObserver(context.Context, string, string, router.Observer) (router.Streamer, bool)
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
	embeddings     http.Handler
	quota          QuotaGate
	rateLimit      ratelimit.Gate
	reservations   reservation.StreamGate
	recorder       *observability.Recorder
	admission      *admission
	cache          *semanticcache.Cache
	cacheRoute     func(context.Context, string, string) string
	cacheHit       func(context.Context, string, string) error
	resolveImages  func(context.Context, string, []provider.Message) error
}

func (s *Server) SetImageResolver(resolve func(context.Context, string, []provider.Message) error) {
	s.resolveImages = resolve
}
func (s *Server) imageResolver(r *http.Request) func([]provider.Message) error {
	if s.resolveImages == nil {
		return nil
	}
	return func(messages []provider.Message) error {
		id, ok := auth.TenantID(r.Context())
		if !ok {
			return provider.ErrProtocol
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		return s.resolveImages(ctx, id, messages)
	}
}

func New(streamer router.Streamer) *Server { return NewWithHealth(streamer) }

func NewWithHealth(streamer router.Streamer, providers ...provider.Provider) *Server {
	return &Server{router: streamer, providers: append([]provider.Provider(nil), providers...), quota: allowQuotaGate{}}
}

// NewWithTenantRouting keeps route declarations separate from construction:
// Resolver receives the actual adapters built by runtime.Build.
func NewWithTenantRouting(resolver TenantResolver, quota ...QuotaGate) *Server {
	return NewWithTenantRoutingAndRecorder(resolver, nil, quota...)
}

func NewWithTenantRoutingAndRecorder(resolver TenantResolver, recorder *observability.Recorder, quota ...QuotaGate) *Server {
	return NewWithTenantRoutingAndRecorderAndReservations(resolver, recorder, nil, quota...)
}

// NewWithTenantRoutingAndRecorderAndReservations adds the opt-in durable
// reservation gate without weakening the existing allow-only development mode.
func NewWithTenantRoutingAndRecorderAndReservations(resolver TenantResolver, recorder *observability.Recorder, reservations reservation.StreamGate, quota ...QuotaGate) *Server {
	gate := QuotaGate(allowQuotaGate{})
	if len(quota) == 1 && quota[0] != nil {
		gate = quota[0]
	}
	return &Server{tenantResolver: resolver, quota: gate, reservations: reservations, recorder: recorder}
}

// SetRateGate installs a process-local admission gate before the server begins
// serving requests. A nil gate leaves the existing default behaviour intact.
func (s *Server) SetRateGate(gate ratelimit.Gate) {
	s.rateLimit = gate
}
func (s *Server) SetSemanticCache(cache *semanticcache.Cache, route func(context.Context, string, string) string, hit func(context.Context, string, string) error) {
	s.cache = cache
	s.cacheRoute = route
	s.cacheHit = hit
}

// SetEmbeddingHandler installs the authenticated, non-streaming embedding
// endpoint. Keeping the handler optional preserves existing chat-only tests
// and deployments while allowing cmd/api to expose the gateway capability.
func (s *Server) SetEmbeddingHandler(handler http.Handler) {
	s.embeddings = handler
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(healthPath, s.handleHealth)
	mux.HandleFunc("/health/providers", s.handleProviderHealth)
	mux.HandleFunc(chatPath, s.handleChat)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/models/{model}", s.handleModels)
	if s.embeddings != nil {
		mux.Handle("/v1/embeddings", s.embeddings)
	}
	return mux
}

// AuthenticatedHandler leaves liveness public but places authentication before
// body decoding, tenant routing, health checks, and provider attempts.
func (s *Server) AuthenticatedHandler(authenticate func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(healthPath, s.handleHealth)
	mux.Handle("/health/providers", authenticate(http.HandlerFunc(s.handleProviderHealth)))
	mux.Handle(chatPath, authenticate(s.withConcurrency(s.withRateLimit(http.HandlerFunc(s.handleChat)))))
	mux.Handle("/v1/models", authenticate(http.HandlerFunc(s.handleModels)))
	mux.Handle("/v1/models/{model}", authenticate(http.HandlerFunc(s.handleModels)))
	mux.Handle("/v1/messages", anthropicAuth(authenticate, s.withConcurrency(s.withRateLimit(http.HandlerFunc(s.handleMessages)))))
	if s.embeddings != nil {
		mux.Handle("/v1/embeddings", authenticate(s.withConcurrency(s.withRateLimit(s.embeddings))))
	}
	mux.Handle(tracePath, authenticate(http.HandlerFunc(s.handleTrace)))
	return mux
}

// withRateLimit executes only after authentication has populated the tenant
// context and before the chat handler can decode a request body.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || s.rateLimit == nil {
			next.ServeHTTP(w, request)
			return
		}
		tenantID, ok := auth.TenantID(request.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, auth.CodeFailed)
			return
		}
		decision := s.rateLimit.Admit(request.Context(), tenantID)
		if decision.Allowed {
			next.ServeHTTP(w, request)
			return
		}
		seconds := int64(math.Ceil(decision.RetryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeJSONError(w, http.StatusTooManyRequests, rateLimited)
	})
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
		providers = s.tenantResolver.VisibleProviders(request.Context(), tenantID)
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
	provider.ChatRequest
	Stream              bool `json:"stream"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
	N                   int  `json:"n,omitempty"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

func (s *Server) handleChat(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	input, err := decodeChatRequest(request, s.imageResolver(request))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_chat_request")
		return
	}
	if endpoint, ok := s.embeddings.(interface{ BillableModel(string) bool }); ok && endpoint.BillableModel(input.Model) {
		writeJSONError(w, http.StatusBadRequest, "embedding_model_not_chat")
		return
	}

	streamer := s.router
	var session *observability.Session
	var tenantID string
	if s.tenantResolver != nil {
		var ok bool
		tenantID, ok = auth.TenantID(request.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, auth.CodeFailed)
			return
		}
		if s.recorder != nil {
			session = s.recorder.Start(tenantID, input.Model)
			if session.TraceID() != "" {
				w.Header().Set(traceHeader, session.TraceID())
			}
		}
		var allowed bool
		if observed, ok := s.tenantResolver.(observedTenantResolver); ok && session != nil {
			streamer, allowed = observed.StreamerWithObserver(request.Context(), tenantID, input.Model, session)
		} else {
			streamer, allowed = s.tenantResolver.Streamer(request.Context(), tenantID, input.Model)
		}
		if !allowed {
			completeTrace(session, modelNotAllowed, false)
			writeJSONError(w, http.StatusForbidden, modelNotAllowed)
			return
		}
		if !s.quota.Allow(request.Context(), tenantID, input.Model) {
			completeTrace(session, quotaExhausted, false)
			writeJSONError(w, http.StatusTooManyRequests, quotaExhausted)
			return
		}
	}
	if streamer == nil {
		completeTrace(session, "provider_unavailable", false)
		writeJSONError(w, http.StatusServiceUnavailable, "provider_unavailable")
		return
	}
	var reservationSession reservation.StreamSession
	chat := input.ChatRequest
	out := newCompletion(w, input.Model, input.Stream)
	if input.StreamOptions != nil {
		out.includeUsage = input.StreamOptions.IncludeUsage
	}
	if provider.HasImages(chat.Messages) || provider.HasTools(chat) {
		capable, ok := streamer.(interface {
			SupportsInput(provider.ChatRequest) bool
		})
		if !ok || !capable.SupportsInput(chat) {
			completeTrace(session, "unsupported_model_input", false)
			writeJSONError(w, 400, "unsupported_model_input")
			return
		}
	}
	var cached *semanticcache.Prepared
	if s.cache != nil && s.cacheRoute != nil && request.Header.Get("X-AgentMesh-Cache") == "semantic" {
		step, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		p, e := s.cache.Prepare(step, tenantID, request.Header.Get("X-AgentMesh-Cache-Namespace"), s.cacheRoute(step, tenantID, input.Model), chat)
		if e == nil {
			cached = &p
			text, hit, getErr := s.cache.Get(step, p)
			if getErr == nil && hit && (s.cacheHit == nil || s.cacheHit(step, tenantID, input.Model) == nil) {
				cancel()
				w.Header().Set("X-AgentMesh-Cache", "hit")
				_ = out.emit(provider.Chunk{Delta: text, Usage: &provider.Usage{}})
				_ = out.complete()
				completeTrace(session, "cache_hit", false)
				return
			}
		}
		cancel()
	}
	if s.reservations != nil {
		reservationSession, err = s.reservations.Begin(request.Context(), tenantID, input.Model, provider.BudgetMessages(chat))
		if err != nil {
			if reservation.Code(err) == quotaExhausted || errors.Is(err, billing.ErrInsufficient) {
				completeTrace(session, quotaExhausted, false)
				writeJSONError(w, http.StatusTooManyRequests, quotaExhausted)
				return
			}
			completeTrace(session, quotaUnavailable, false)
			writeJSONError(w, http.StatusServiceUnavailable, quotaUnavailable)
			return
		}
	}
	if bill, ok := reservationSession.(interface{ BillingID() string }); ok {
		w.Header().Set("X-AgentMesh-Billing-Request-ID", bill.BillingID())
	}
	var cacheText strings.Builder
	cacheable := cached != nil
	var finalUsage *provider.Usage
	emit := func(chunk provider.Chunk) error {
		if cacheable {
			if cacheText.Len()+len(chunk.Delta) > 64<<10 {
				cacheable = false
			} else {
				cacheText.WriteString(chunk.Delta)
			}
		}
		if chunk.Usage != nil {
			u := *chunk.Usage
			finalUsage = &u
		}
		return out.emit(chunk)
	}
	if reservationSession != nil {
		hooked, ok := streamer.(router.HookedStreamer)
		if !ok {
			_ = reservationSession.Complete(request.Context(), quotaUnavailable, false)
			completeTrace(session, quotaUnavailable, false)
			writeJSONError(w, http.StatusServiceUnavailable, quotaUnavailable)
			return
		}
		err = hooked.StreamWithAttemptHook(request.Context(), chat, emit, reservationSession)
	} else {
		err = streamer.Stream(request.Context(), chat, emit)
	}
	if reservationSession != nil {
		if completeErr := reservationSession.Complete(request.Context(), streamErrorCode(err), errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)); completeErr != nil {
			completeTrace(session, quotaUnavailable, false)
			out.fail(quotaUnavailable)
			return
		}
	}
	if err == nil {
		if cacheable && finalUsage != nil && !finalUsage.Estimated {
			step, cancel := context.WithTimeout(request.Context(), time.Second)
			_ = s.cache.Put(step, *cached, cacheText.String())
			cancel()
		}
		completeTrace(session, "success", false)
		if out.complete() != nil {
			out.fail("provider_protocol_error")
		}
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		completeTrace(session, "cancelled", true)
		return
	}
	completeTrace(session, streamErrorCode(err), false)
	out.fail(streamErrorCode(err))
}

func (s *Server) handleTrace(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	tenantID, ok := auth.TenantID(request.Context())
	if !ok || s.recorder == nil {
		writeJSONError(w, http.StatusNotFound, traceNotFound)
		return
	}
	trace, ok := s.recorder.Get(tenantID, request.PathValue("trace_id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, traceNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(trace)
}

func completeTrace(session *observability.Session, resultCode string, cancelled bool) {
	if session != nil {
		session.Complete(resultCode, cancelled)
	}
}

func decodeChatRequest(request *http.Request, resolve ...func([]provider.Message) error) (chatRequest, error) {
	defer request.Body.Close()
	data, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
	if err != nil || len(data) > maxRequestBytes {
		return chatRequest{}, errors.New("body too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input chatRequest
	if err := decoder.Decode(&input); err != nil {
		return chatRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return chatRequest{}, errors.New("trailing JSON")
	}
	if strings.TrimSpace(input.Model) == "" || len(input.Messages) == 0 {
		return chatRequest{}, errors.New("missing required chat fields")
	}
	if input.MaxCompletionTokens != nil {
		if input.MaxTokens != 0 && input.MaxTokens != *input.MaxCompletionTokens {
			return chatRequest{}, errors.New("conflicting token bounds")
		}
		input.MaxTokens = *input.MaxCompletionTokens
	}
	if input.N != 0 && input.N != 1 {
		return chatRequest{}, errors.New("only one completion supported")
	}
	if input.MaxTokens < 0 || input.MaxTokens > 4096 {
		return chatRequest{}, errors.New("invalid output limit")
	}
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2 || math.IsNaN(*input.Temperature) || math.IsInf(*input.Temperature, 0)) {
		return chatRequest{}, errors.New("invalid temperature")
	}
	if input.MaxTokens == 0 {
		input.MaxTokens = 1024
	}
	if len(resolve) > 0 && resolve[0] != nil {
		if e := resolve[0](input.Messages); e != nil {
			return chatRequest{}, e
		}
	}
	if provider.ValidateChat(input.ChatRequest) != nil {
		return chatRequest{}, errors.New("invalid messages")
	}
	return input, nil
}

func streamErrorCode(err error) string {
	if reservation.Code(err) == quotaExhausted {
		return quotaExhausted
	}
	if errors.Is(err, router.ErrStreamInterrupted) {
		return "stream_interrupted"
	}
	return "provider_unavailable"
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	kind := "invalid_request_error"
	if status == 401 {
		kind = "authentication_error"
	} else if status == 429 {
		kind = "rate_limit_error"
	} else if status >= 500 {
		kind = "api_error"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code, "type": kind}})
}

func writeSSE(w http.ResponseWriter, payload any) error {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Second))
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
