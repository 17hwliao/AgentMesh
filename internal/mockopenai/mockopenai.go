// Package mockopenai provides a small OpenAI-compatible test upstream.
package mockopenai

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	SmallModel  = "mock-small"
	VisionModel = "mock-vision"

	maxRequestBytes   = 2 << 20
	maxSSETextBytes   = 16 << 10
	maxToolInputBytes = 4 << 10
)

// Server is a thread-safe OpenAI-compatible mock upstream.
type Server struct {
	*httptest.Server

	mu            sync.Mutex
	requests      int
	statusByModel map[string]int
	nextStatus    int
	delay         time.Duration
	authToken     string
}

// New starts and returns a mock upstream.
func New() *Server {
	server := &Server{statusByModel: make(map[string]int)}
	server.Server = httptest.NewServer(server)
	return server
}

// URL returns the base URL of the mock upstream.
func (s *Server) URL() string {
	return s.Server.URL
}

// Close shuts down the mock upstream.
func (s *Server) Close() {
	if s != nil && s.Server != nil {
		s.Server.Close()
	}
}

// Requests returns the number of HTTP requests received since New or Reset.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Reset clears request counts and all test controls.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = 0
	s.statusByModel = make(map[string]int)
	s.nextStatus = 0
	s.delay = 0
	s.authToken = ""
}

// SetStatus makes requests for model return status until cleared or
// ResetFailures is called. A status of zero clears the model override.
func (s *Server) SetStatus(model string, status int) {
	if status != 0 && (status < http.StatusBadRequest || status > 599) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == 0 {
		delete(s.statusByModel, model)
		return
	}
	if s.statusByModel == nil {
		s.statusByModel = make(map[string]int)
	}
	s.statusByModel[model] = status
}

// SetNextStatus makes the next chat completion return status. A status of zero
// clears the pending override.
func (s *Server) SetNextStatus(status int) {
	if status != 0 && (status < http.StatusBadRequest || status > 599) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextStatus = status
}

// SetDelay sets a delay applied before each routed API response.
func (s *Server) SetDelay(delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = delay
}

// SetAuthToken enables Bearer authentication for all API endpoints. An empty
// token disables authentication.
func (s *Server) SetAuthToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authToken = token
}

// ResetFailures clears forced statuses while preserving delay, authentication,
// and request counts.
func (s *Server) ResetFailures() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusByModel = make(map[string]int)
	s.nextStatus = 0
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	meta := s.beginRequest()
	if !wait(r.Context(), meta.delay) {
		return
	}
	if meta.authToken != "" && !authorized(r.Header.Get("Authorization"), meta.authToken) {
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid mock bearer token")
		return
	}

	switch r.URL.Path {
	case "/models", "/v1/models":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		s.writeModels(w)
	case "/chat/completions", "/v1/chat/completions":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if status := s.takeNextStatus(); status != 0 {
			writeForcedStatus(w, status)
			return
		}
		request, err := decodeChatRequest(w, r)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request")
			return
		}
		if status := s.modelStatus(request.Model); status != 0 {
			writeForcedStatus(w, status)
			return
		}
		s.writeChatResponse(w, request, meta.sequence)
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

type requestMeta struct {
	delay     time.Duration
	authToken string
	sequence  int
}

func (s *Server) beginRequest() requestMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	return requestMeta{delay: s.delay, authToken: s.authToken, sequence: s.requests}
}

func (s *Server) takeNextStatus() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.nextStatus
	s.nextStatus = 0
	return status
}

func (s *Server) modelStatus(model string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusByModel[model]
}

func wait(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func authorized(header, token string) bool {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(token)) == 1
}

type modelList struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (s *Server) writeModels(w http.ResponseWriter) {
	created := time.Now().Unix()
	writeJSON(w, http.StatusOK, modelList{
		Object: "list",
		Data: []modelInfo{
			{ID: SmallModel, Object: "model", Created: created, OwnedBy: "mockopenai"},
			{ID: VisionModel, Object: "model", Created: created, OwnedBy: "mockopenai"},
		},
	})
}

type chatRequest struct {
	Model               string        `json:"model"`
	Stream              bool          `json:"stream"`
	Messages            []chatMessage `json:"messages"`
	MaxTokens           int           `json:"max_tokens"`
	MaxCompletionTokens int           `json:"max_completion_tokens"`
	Tools               []chatTool    `json:"tools"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

func decodeChatRequest(w http.ResponseWriter, r *http.Request) (chatRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var request chatRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		return chatRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return chatRequest{}, err
	}
	return request, nil
}

func (s *Server) writeChatResponse(w http.ResponseWriter, request chatRequest, sequence int) {
	text := responseText(request)
	toolName, toolArguments, hasTool := responseTool(request)
	usage := responseUsage(request, text, toolName, toolArguments)
	id := fmt.Sprintf("chatcmpl-mock-%d", sequence)
	created := time.Now().Unix()

	if request.Stream {
		writeChatStream(w, id, created, request.Model, text, toolName, toolArguments, hasTool, usage)
		return
	}
	writeChatCompletion(w, id, created, request.Model, text, toolName, toolArguments, hasTool, usage)
}

func responseText(request chatRequest) string {
	if request.Model == SmallModel {
		return "hello from mock"
	}
	return "mock echo: " + lastUserText(request.Messages)
}

func responseTool(request chatRequest) (string, string, bool) {
	if len(request.Tools) == 0 {
		return "", "", false
	}
	name := "mock_tool"
	for _, tool := range request.Tools {
		if tool.Function.Name != "" {
			name = tool.Function.Name
			break
		}
	}
	arguments, err := json.Marshal(map[string]string{"input": truncateUTF8(lastUserText(request.Messages), maxToolInputBytes)})
	if err != nil {
		arguments = []byte(`{"input":""}`)
	}
	return name, string(arguments), true
}

func lastUserText(messages []chatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messageText(messages[i].Content)
		}
	}
	return ""
}

func messageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	var textBuilder strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			textBuilder.WriteString(part.Text)
		}
	}
	return textBuilder.String()
}

type completionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func responseUsage(request chatRequest, text, toolName, toolArguments string) completionUsage {
	promptTokens := 1
	for _, message := range request.Messages {
		promptTokens += 4 + estimateTokens(messageText(message.Content))
	}
	completionText := text
	if toolName != "" {
		completionText = toolName + toolArguments
	}
	completionTokens := estimateTokens(completionText)
	return completionUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

func estimateTokens(text string) int {
	runes := 0
	for range text {
		runes++
	}
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

type completionChunk struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *completionUsage   `json:"usage,omitempty"`
}

type completionChoice struct {
	Index        int             `json:"index"`
	Delta        completionDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

type completionDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
}

type toolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function callFunction `json:"function"`
}

type callFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type completionToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function callFunction `json:"function"`
}

func writeChatStream(w http.ResponseWriter, id string, created int64, model, text, toolName, toolArguments string, hasTool bool, usage completionUsage) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	empty := ""
	if err := writeSSE(w, completionChunk{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []completionChoice{{Index: 0, Delta: completionDelta{Role: "assistant", Content: &empty}}},
	}); err != nil {
		return
	}

	if hasTool {
		if err := writeSSE(w, completionChunk{
			ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []completionChoice{{Index: 0, Delta: completionDelta{ToolCalls: []toolCallDelta{{
				Index: 0, ID: fmt.Sprintf("call_mock_%s", strings.TrimPrefix(id, "chatcmpl-mock-")), Type: "function",
				Function: callFunction{Name: toolName, Arguments: toolArguments},
			}}}}},
		}); err != nil {
			return
		}
	} else {
		for _, part := range splitText(text, maxSSETextBytes) {
			content := part
			if err := writeSSE(w, completionChunk{
				ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
				Choices: []completionChoice{{Index: 0, Delta: completionDelta{Content: &content}}},
			}); err != nil {
				return
			}
		}
	}

	finishReason := "stop"
	if hasTool {
		finishReason = "tool_calls"
	}
	if err := writeSSE(w, completionChunk{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []completionChoice{{Index: 0, FinishReason: &finishReason}},
	}); err != nil {
		return
	}
	if err := writeSSE(w, completionChunk{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
		Choices: []completionChoice{}, Usage: &usage,
	}); err != nil {
		return
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flush(w)
}

func writeChatCompletion(w http.ResponseWriter, id string, created int64, model, text, toolName, toolArguments string, hasTool bool, usage completionUsage) {
	message := map[string]any{"role": "assistant", "content": text}
	finishReason := "stop"
	if hasTool {
		message["content"] = nil
		message["tool_calls"] = []completionToolCall{{
			ID:       fmt.Sprintf("call_mock_%s", strings.TrimPrefix(id, "chatcmpl-mock-")),
			Type:     "function",
			Function: callFunction{Name: toolName, Arguments: toolArguments},
		}}
		finishReason = "tool_calls"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": usage,
	})
}

func writeSSE(w http.ResponseWriter, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flush(w)
	return nil
}

func splitText(text string, limit int) []string {
	var parts []string
	for len(text) > limit {
		end := limit
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		if end == 0 {
			end = limit
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	return append(parts, text)
}

func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	if end == 0 {
		return text[:limit]
	}
	return text[:end]
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeForcedStatus(w http.ResponseWriter, status int) {
	code := "mock_error"
	message := "mock upstream failure"
	switch status {
	case http.StatusUnauthorized:
		code = "mock_unauthorized"
		message = "mock unauthorized response"
	case http.StatusTooManyRequests:
		code = "rate_limit_exceeded"
		message = "mock rate limit exceeded"
		w.Header().Set("Retry-After", "1")
	}
	writeError(w, status, code, message)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	kind := "invalid_request_error"
	if status == http.StatusTooManyRequests {
		kind = "rate_limit_error"
	} else if status == http.StatusUnauthorized {
		kind = "authentication_error"
	} else if status >= http.StatusInternalServerError {
		kind = "server_error"
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    kind,
			"param":   nil,
			"code":    code,
		},
	})
}
