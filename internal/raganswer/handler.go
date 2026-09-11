package raganswer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/tenant"
)

type Handler struct {
	store     tenant.Store
	providers map[string]Provider
	now       func() time.Time
}

func NewHandler(store tenant.Store, providers []Provider) (*Handler, error) {
	if store == nil || len(providers) == 0 {
		return nil, errors.New("rag answer handler requires tenant store and provider")
	}
	byName := make(map[string]Provider, len(providers))
	for _, provider := range providers {
		if provider == nil || strings.TrimSpace(provider.Name()) == "" {
			return nil, errors.New("rag answer provider is invalid")
		}
		if _, exists := byName[provider.Name()]; exists {
			return nil, errors.New("duplicate rag answer provider")
		}
		byName[provider.Name()] = provider
	}
	return &Handler{store: store, providers: byName, now: func() time.Time { return time.Now().UTC() }}, nil
}

type httpRequest struct {
	Model    string         `json:"model"`
	Question string         `json:"question"`
	Contexts []ContextChunk `json:"contexts"`
}

type httpResponse struct {
	Object      string `json:"object"`
	Model       string `json:"model"`
	Facts       []Fact `json:"facts,omitempty"`
	RefusalCode string `json:"refusal_code,omitempty"`
	TraceID     string `json:"trace_id"`
	Provider    string `json:"provider"`
	Usage       usage  `json:"usage"`
}

type usage struct {
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
	DurationMS   int64 `json:"duration_ms"`
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
	request, err := decodeRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_rag_answer_request")
		return
	}
	if prohibited(request.Question) {
		writeResponse(w, http.StatusOK, httpResponse{Object: "rag.answer", Model: request.Model, RefusalCode: "out_of_scope", TraceID: newTraceID(), Provider: "policy"})
		return
	}
	route, allowed := h.store.Route(r.Context(), tenantID, request.Model)
	if !allowed {
		writeError(w, http.StatusForbidden, "model_not_allowed")
		return
	}
	provider, ok := firstProvider(route, h.providers)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "rag_answer_provider_unavailable")
		return
	}
	traceID := newTraceID()
	w.Header().Set("X-AgentMesh-Trace-ID", traceID)
	started := h.now()
	// Per-call deadline prevents an adapter from inheriting an unlimited server
	// write lifetime. A real adapter may provide its validated upstream timeout;
	// a caller's shorter deadline remains authoritative.
	timeout := 20 * time.Second
	if configured, ok := provider.(deadlineProvider); ok && configured.CallTimeout() > 0 {
		timeout = configured.CallTimeout()
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	result, err := provider.Answer(ctx, request)
	if err != nil {
		writeError(w, providerStatus(err), providerCode(err))
		return
	}
	if containsProhibitedFact(result.Facts) {
		// Do not forward a model-generated SQL/DDL/performance response even if
		// it carries a citation. This is a policy refusal, not provider evidence.
		writeResponse(w, http.StatusOK, httpResponse{Object: "rag.answer", Model: request.Model, RefusalCode: "out_of_scope", TraceID: traceID, Provider: "policy", Usage: usage{DurationMS: h.now().Sub(started).Milliseconds()}})
		return
	}
	if err := validateResult(result, request.Contexts); err != nil {
		writeError(w, http.StatusBadGateway, "rag_answer_provider_protocol_error")
		return
	}
	writeResponse(w, http.StatusOK, httpResponse{Object: "rag.answer", Model: request.Model, Facts: result.Facts, RefusalCode: result.RefusalCode, TraceID: traceID, Provider: provider.Name(), Usage: usage{InputTokens: nonNegative(result.InputTokens), OutputTokens: nonNegative(result.OutputTokens), DurationMS: h.now().Sub(started).Milliseconds()}})
}

func decodeRequest(r *http.Request) (Request, error) {
	if r.ContentLength > 96<<10 {
		return Request{}, ErrRejected
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 96<<10+1))
	decoder.DisallowUnknownFields()
	var input httpRequest
	if err := decoder.Decode(&input); err != nil {
		return Request{}, ErrRejected
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Request{}, ErrRejected
	}
	if strings.TrimSpace(input.Model) == "" || strings.TrimSpace(input.Question) == "" || len([]byte(input.Question)) > MaxQuestionBytes || len(input.Contexts) > MaxContexts {
		return Request{}, ErrRejected
	}
	for index := range input.Contexts {
		context := &input.Contexts[index]
		if !validCitation(context.Citation) || strings.TrimSpace(context.Content) == "" || len([]byte(context.Content)) > MaxContextBytes || context.Hash != contentHash(context.Content) {
			return Request{}, ErrRejected
		}
	}
	return Request{Model: input.Model, Question: input.Question, Contexts: input.Contexts}, nil
}

func validCitation(c Citation) bool {
	return strings.TrimSpace(c.SourceURI) != "" && strings.TrimSpace(c.DocumentID) != "" && strings.TrimSpace(c.Version) != "" && strings.TrimSpace(c.ChunkID) != "" && strings.TrimSpace(c.Hash) != ""
}

func validateResult(result Result, contexts []ContextChunk) error {
	if result.RefusalCode != "" {
		if len(result.Facts) != 0 || result.RefusalCode != "insufficient_evidence" {
			return ErrProtocol
		}
		return nil
	}
	if len(result.Facts) == 0 {
		return ErrProtocol
	}
	allowed := make(map[Citation]struct{}, len(contexts))
	for _, chunk := range contexts {
		allowed[chunk.Citation] = struct{}{}
	}
	for _, fact := range result.Facts {
		if strings.TrimSpace(fact.Text) == "" || len(fact.Citations) == 0 {
			return ErrProtocol
		}
		for _, citation := range fact.Citations {
			if !validCitation(citation) {
				return ErrProtocol
			}
			if _, ok := allowed[citation]; !ok {
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

func prohibited(question string) bool {
	question = strings.ToLower(question)
	for _, term := range []string{"select ", "insert ", "update ", "delete ", "create table", "alter table", "drop table", "ddl", "sql", "explain plan", "query plan", "index recommendation", "performance tuning", "性能", "调优", "性能裁决", "执行计划", "索引建议", "建表", "删表"} {
		if strings.Contains(question, term) {
			return true
		}
	}
	return false
}

func containsProhibitedFact(facts []Fact) bool {
	for _, fact := range facts {
		if prohibitedGeneratedContent(fact.Text) {
			return true
		}
	}
	return false
}

// prohibitedGeneratedContent is intentionally narrower than prohibited. The
// latter guards a caller's request; applying it verbatim to an answer made a
// factual safety explanation such as "do not generate SQL" indistinguishable
// from generated SQL. Here we reject executable-looking statements and direct
// index advice while allowing an evidence-backed description of the boundary.
func prohibitedGeneratedContent(text string) bool {
	value := strings.ToLower(text)
	if strings.Contains(value, "```sql") {
		return true
	}
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*•> `"))
		for _, prefix := range []string{
			"select ", "insert into ", "update ", "delete from ",
			"create table ", "alter table ", "drop table ", "create index ",
			"execute select ", "execute insert ", "execute update ", "execute delete ",
		} {
			if strings.HasPrefix(line, prefix) {
				return true
			}
		}
	}
	for _, advice := range []string{"add an index", "create an index", "recommend an index", "建议创建索引", "建议添加索引"} {
		if strings.Contains(value, advice) {
			return true
		}
	}
	return false
}

func providerStatus(err error) int {
	if errors.Is(err, ErrProtocol) {
		return http.StatusBadGateway
	}
	return http.StatusServiceUnavailable
}

func providerCode(err error) string {
	if errors.Is(err, ErrProtocol) {
		return "rag_answer_provider_protocol_error"
	}
	return "rag_answer_provider_unavailable"
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func newTraceID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "rag-answer-trace-unavailable"
	}
	return hex.EncodeToString(bytes)
}

func writeResponse(w http.ResponseWriter, status int, value httpResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}
