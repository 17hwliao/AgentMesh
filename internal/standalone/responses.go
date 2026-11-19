package standalone

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentmesh/internal/auth"
	"agentmesh/internal/provider"
)

type responsesRequest struct {
	Model           string            `json:"model"`
	Input           json.RawMessage   `json:"input"`
	Stream          bool              `json:"stream,omitempty"`
	Instructions    string            `json:"instructions,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type responseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (a *App) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		writeError(w, http.StatusBadRequest, "invalid_response_request")
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_response_request")
		return
	}
	for _, field := range []string{"tools", "tool_choice", "previous_response_id", "store", "reasoning", "text", "include", "background", "truncation"} {
		if _, exists := object[field]; exists {
			writeError(w, http.StatusUnprocessableEntity, "unsupported_response_field:"+field)
			return
		}
	}
	var input responsesRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_response_request")
		return
	}
	if strings.TrimSpace(input.Model) == "" {
		writeError(w, http.StatusBadRequest, "model_required")
		return
	}
	messages, err := decodeResponseMessages(input.Input)
	if err != nil || len(messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_response_input")
		return
	}
	if input.Instructions != "" {
		messages = append([]provider.Message{{Role: "system", Content: input.Instructions}}, messages...)
	}
	limit := input.MaxOutputTokens
	if limit <= 0 {
		limit = 1024
	}
	if limit > 4096 {
		writeError(w, http.StatusBadRequest, "invalid_max_output_tokens")
		return
	}
	if input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) {
		writeError(w, http.StatusBadRequest, "invalid_temperature")
		return
	}
	tenantID, ok := auth.TenantID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "auth_failed")
		return
	}
	if !a.manager.Allow(r.Context(), tenantID, input.Model) {
		writeError(w, http.StatusTooManyRequests, "quota_exhausted")
		return
	}
	streamer, allowed := a.resolver.Streamer(r.Context(), tenantID, input.Model)
	if !allowed {
		writeError(w, http.StatusForbidden, "model_not_allowed")
		return
	}
	request := provider.ChatRequest{Model: input.Model, Messages: messages, MaxTokens: limit, Temperature: input.Temperature}
	if provider.ValidateChat(request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_response_input")
		return
	}
	id := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	created := time.Now().Unix()
	var text strings.Builder
	var usage provider.Usage
	var sawUsage bool
	emit := func(chunk provider.Chunk) error {
		if len(chunk.ToolCalls) > 0 {
			return errors.New("unsupported_tool_output")
		}
		if chunk.Usage != nil {
			usage, sawUsage = *chunk.Usage, true
		}
		if chunk.Delta != "" {
			text.WriteString(chunk.Delta)
			if input.Stream {
				if err := writeResponseEvent(w, "response.output_text.delta", map[string]any{
					"type": "response.output_text.delta", "response_id": id, "delta": chunk.Delta,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if input.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		_ = writeResponseEvent(w, "response.created", map[string]any{
			"type": "response.created", "response": responseObject(id, created, input.Model, "in_progress", "", nil),
		})
	}
	err = streamer.Stream(r.Context(), request, emit)
	if err != nil {
		if input.Stream {
			_ = writeResponseEvent(w, "response.failed", map[string]any{
				"type": "response.failed", "response": responseObject(id, created, input.Model, "failed", "", nil),
			})
			return
		}
		writeError(w, http.StatusBadGateway, "provider_unavailable")
		return
	}
	finalUsage := map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	if sawUsage {
		finalUsage = map[string]any{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "total_tokens": usage.InputTokens + usage.OutputTokens}
	}
	result := responseObject(id, created, input.Model, "completed", text.String(), finalUsage)
	if input.Stream {
		_ = writeResponseEvent(w, "response.output_text.done", map[string]any{
			"type": "response.output_text.done", "response_id": id, "text": text.String(),
		})
		_ = writeResponseEvent(w, "response.completed", map[string]any{
			"type": "response.completed", "response": result,
		})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeResponseMessages(raw json.RawMessage) ([]provider.Message, error) {
	if len(raw) == 0 {
		return nil, errors.New("input_required")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []provider.Message{{Role: "user", Content: text}}, nil
	}
	var rows []responseMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]provider.Message, 0, len(rows))
	for _, row := range rows {
		role := strings.TrimSpace(row.Role)
		if role == "" {
			role = "user"
		}
		if role != "user" && role != "assistant" && role != "system" {
			return nil, errors.New("invalid_role")
		}
		if row.Content == "" {
			return nil, errors.New("empty_content")
		}
		out = append(out, provider.Message{Role: role, Content: row.Content})
	}
	return out, nil
}

func responseObject(id string, created int64, model, status, text string, usage map[string]any) map[string]any {
	output := []any{}
	if text != "" {
		output = append(output, map[string]any{
			"type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		})
	}
	return map[string]any{
		"id": id, "object": "response", "created_at": created, "status": status,
		"model": model, "output": output, "usage": usage,
	}
}

func writeResponseEvent(w http.ResponseWriter, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
