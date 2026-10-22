package gateway

import (
	"agentmesh/internal/provider"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Both external auth styles feed the existing tenant authentication once.
func anthropicAuth(authenticate func(http.Handler) http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		bearer := r.Header.Get("Authorization")
		if key != "" && bearer != "" && bearer != "Bearer "+key {
			anthropicError(w, 401, "authentication_error", "conflicting_credentials")
			return
		}
		clone := r.Clone(r.Context())
		clone.Header = r.Header.Clone()
		if key != "" && bearer == "" {
			clone.Header.Set("Authorization", "Bearer "+key)
		}
		authenticate(next).ServeHTTP(&anthropicErrorWriter{ResponseWriter: w}, clone)
	})
}
func anthropicError(w http.ResponseWriter, status int, kind, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": code}})
}
func anthropicErrorType(status int) string {
	switch status {
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 429:
		return "rate_limit_error"
	case 400, 405, 413:
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

type anthropicErrorWriter struct {
	http.ResponseWriter
	status int
}

func (w *anthropicErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *anthropicErrorWriter) WriteHeader(n int) { w.status = n; w.ResponseWriter.WriteHeader(n) }
func (w *anthropicErrorWriter) Write(p []byte) (int, error) {
	if w.status >= 400 {
		var body map[string]any
		if json.Unmarshal(p, &body) == nil {
			raw, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": anthropicErrorType(w.status), "message": safeError(body)}})
			_, e := w.ResponseWriter.Write(append(raw, '\n'))
			return len(p), e
		}
	}
	return w.ResponseWriter.Write(p)
}
func (w *anthropicErrorWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func safeError(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		if s, ok := e["code"].(string); ok {
			return s
		}
		if s, ok := e["message"].(string); ok {
			return s
		}
	}
	return "request_failed"
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
	CacheControl json.RawMessage `json:"cache_control"`
}
type anthropicRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	System        json.RawMessage `json:"system"`
	MaxTokens     int             `json:"max_tokens"`
	Stream        bool            `json:"stream"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	StopSequences []string        `json:"stop_sequences"`
	Tools         []struct {
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		InputSchema  json.RawMessage `json:"input_schema"`
		CacheControl json.RawMessage `json:"cache_control"`
		Type         string          `json:"type"`
	} `json:"tools"`
	ToolChoice *struct {
		Type            string `json:"type"`
		Name            string `json:"name"`
		DisableParallel bool   `json:"disable_parallel_tool_use"`
	} `json:"tool_choice"`
	Metadata     json.RawMessage `json:"metadata"`
	Thinking     json.RawMessage `json:"thinking"`
	OutputConfig json.RawMessage `json:"output_config"`
}

func textBlocks(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []anthropicBlock
	if json.Unmarshal(raw, &parts) != nil {
		return "", provider.ErrProtocol
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", provider.ErrProtocol
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}
func convertAnthropic(a anthropicRequest, resolve ...func([]provider.Message) error) (chatRequest, error) {
	fail := func() (chatRequest, error) { return chatRequest{}, provider.ErrProtocol }
	r := chatRequest{ChatRequest: provider.ChatRequest{Model: a.Model, MaxTokens: a.MaxTokens, Temperature: a.Temperature, TopP: a.TopP}, Stream: a.Stream}
	if a.MaxTokens < 1 || a.MaxTokens > 4096 {
		return fail()
	}
	if len(a.Thinking) > 0 {
		var t struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(a.Thinking, &t) != nil || t.Type != "disabled" {
			return fail()
		}
	}
	if len(a.OutputConfig) > 0 {
		var c struct {
			Effort string          `json:"effort"`
			Format json.RawMessage `json:"format"`
		}
		d := json.NewDecoder(bytes.NewReader(a.OutputConfig))
		d.DisallowUnknownFields()
		if d.Decode(&c) != nil || len(c.Format) > 0 {
			return fail()
		}
	}
	if len(a.System) > 0 {
		text, e := textBlocks(a.System)
		if e != nil {
			return fail()
		}
		r.Messages = append(r.Messages, provider.Message{Role: "system", Content: text})
	}
	for _, m := range a.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			return fail()
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			r.Messages = append(r.Messages, provider.Message{Role: m.Role, Content: text})
			continue
		}
		var blocks []anthropicBlock
		if json.Unmarshal(m.Content, &blocks) != nil || len(blocks) == 0 {
			return fail()
		}
		msg := provider.Message{Role: m.Role}
		var parts []provider.ContentPart
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, provider.ContentPart{Type: "text", Text: b.Text})
			case "image":
				if b.Source == nil || m.Role != "user" {
					return fail()
				}
				uri := b.Source.URL
				if b.Source.Type == "base64" {
					uri = "data:" + b.Source.MediaType + ";base64," + b.Source.Data
				} else if b.Source.Type != "url" {
					return fail()
				}
				parts = append(parts, provider.ContentPart{Type: "image_url", ImageURL: &provider.ImageURL{URL: uri}})
			case "tool_use":
				if m.Role != "assistant" {
					return fail()
				}
				msg.ToolCalls = append(msg.ToolCalls, provider.ToolCall{ID: b.ID, Type: "function", Function: provider.CallFunction{Name: b.Name, Arguments: string(b.Input)}})
			case "tool_result":
				if m.Role != "user" {
					return fail()
				}
				text, e := textBlocks(b.Content)
				if e != nil {
					return fail()
				}
				if b.IsError {
					text = "Tool execution error: " + text
				}
				r.Messages = append(r.Messages, provider.Message{Role: "tool", ToolCallID: b.ToolUseID, Content: text})
			default:
				return fail()
			}
		}
		image := false
		for _, p := range parts {
			if p.Type == "image_url" {
				image = true
			}
		}
		if image {
			msg.Parts = parts
		} else {
			for _, p := range parts {
				msg.Content += p.Text
			}
		}
		if msg.Content != "" || msg.Parts != nil || len(msg.ToolCalls) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}
	for _, t := range a.Tools {
		if t.Type != "" && t.Type != "custom" {
			return fail()
		}
		r.Tools = append(r.Tools, provider.ToolDefinition{Type: "function", Function: provider.ToolFunction{Name: t.Name, Description: t.Description, Parameters: t.InputSchema}})
	}
	if a.ToolChoice != nil {
		kind := a.ToolChoice.Type
		switch kind {
		case "auto", "none":
			r.ToolChoice, _ = json.Marshal(kind)
		case "any":
			r.ToolChoice = json.RawMessage(`"required"`)
		case "tool":
			r.ToolChoice, _ = json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": a.ToolChoice.Name}})
		default:
			return fail()
		}
		parallel := !a.ToolChoice.DisableParallel
		r.ParallelToolCalls = &parallel
	}
	if len(a.StopSequences) > 0 {
		r.Stop, _ = json.Marshal(a.StopSequences)
	}
	if len(resolve) > 0 && resolve[0] != nil {
		if e := resolve[0](r.Messages); e != nil {
			return fail()
		}
	}
	if provider.ValidateChat(r.ChatRequest) != nil {
		return fail()
	}
	return r, nil
}
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		anthropicError(w, 405, "invalid_request_error", "method_not_allowed")
		return
	}
	if v := r.Header.Get("anthropic-version"); v != "" && v != "2023-06-01" {
		anthropicError(w, 400, "invalid_request_error", "unsupported_anthropic_version")
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if e != nil || len(raw) > maxRequestBytes {
		anthropicError(w, 400, "invalid_request_error", "request_too_large")
		return
	}
	var input anthropicRequest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF {
		anthropicError(w, 400, "invalid_request_error", "invalid_messages_request")
		return
	}
	converted, e := convertAnthropic(input, s.imageResolver(r))
	if e != nil {
		anthropicError(w, 400, "invalid_request_error", "unsupported_or_invalid_messages")
		return
	}
	if len(input.OutputConfig) > 0 {
		w.Header().Set("X-AgentMesh-Unsupported-Options", "output_config.effort")
	}
	body, _ := json.Marshal(converted)
	clone := r.Clone(r.Context())
	clone.URL.Path = chatPath
	clone.Body = io.NopCloser(bytes.NewReader(body))
	clone.ContentLength = int64(len(body))
	bridge := &anthropicBridge{w: w, stream: input.Stream, model: input.Model, blocks: map[int]int{}, open: map[int]bool{}}
	s.handleChat(bridge, clone)
}

type anthropicBridge struct {
	w                       http.ResponseWriter
	stream, started, failed bool
	status                  int
	model                   string
	buffer                  []byte
	next                    int
	textIndex               *int
	blocks                  map[int]int
	open                    map[int]bool
	finish                  string
	usage                   map[string]any
}

func (b *anthropicBridge) Unwrap() http.ResponseWriter { return b.w }

func (b *anthropicBridge) Header() http.Header { return b.w.Header() }
func (b *anthropicBridge) WriteHeader(n int) {
	b.status = n
	if n >= 400 {
		b.w.Header().Set("Content-Type", "application/json")
	}
	b.w.WriteHeader(n)
}
func (b *anthropicBridge) Flush() {}
func (b *anthropicBridge) event(kind string, value map[string]any) error {
	value["type"] = kind
	raw, _ := json.Marshal(value)
	_, e := fmt.Fprintf(b.w, "event: %s\ndata: %s\n\n", kind, raw)
	if f, ok := b.w.(http.Flusher); ok {
		f.Flush()
	}
	return e
}
func (b *anthropicBridge) start(id string) error {
	if b.started {
		return nil
	}
	b.started = true
	b.w.Header().Set("Content-Type", "text/event-stream")
	return b.event("message_start", map[string]any{"message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": b.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}})
}
func anthropicUsage(raw map[string]any) map[string]any {
	input, _ := raw["prompt_tokens"].(float64)
	output, _ := raw["completion_tokens"].(float64)
	cached := float64(0)
	if details, ok := raw["prompt_tokens_details"].(map[string]any); ok {
		cached, _ = details["cached_tokens"].(float64)
	}
	return map[string]any{"input_tokens": input - cached, "output_tokens": output, "cache_creation_input_tokens": 0, "cache_read_input_tokens": cached}
}
func reason(r string) string {
	switch r {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}
func (b *anthropicBridge) Write(p []byte) (int, error) {
	if b.status >= 400 {
		var row map[string]any
		if json.Unmarshal(p, &row) == nil {
			raw, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": anthropicErrorType(b.status), "message": safeError(row)}})
			_, e := b.w.Write(append(raw, '\n'))
			return len(p), e
		}
		return b.w.Write(p)
	}
	b.buffer = append(b.buffer, p...)
	if len(b.buffer) > 2<<20 {
		return 0, errors.New("response bound")
	}
	if !b.stream {
		var row map[string]any
		if json.Unmarshal(b.buffer, &row) != nil {
			return len(p), nil
		}
		converted, e := anthropicCompletion(row)
		if e != nil {
			return 0, e
		}
		b.buffer = nil
		return len(p), json.NewEncoder(b.w).Encode(converted)
	}
	for {
		i := bytes.Index(b.buffer, []byte("\n\n"))
		if i < 0 {
			break
		}
		line := strings.TrimSpace(strings.TrimPrefix(string(b.buffer[:i]), "data:"))
		b.buffer = b.buffer[i+2:]
		if line == "[DONE]" {
			if b.failed {
				continue
			}
			for idx := 0; idx < b.next; idx++ {
				if b.open[idx] {
					if e := b.event("content_block_stop", map[string]any{"index": idx}); e != nil {
						return 0, e
					}
					b.open[idx] = false
				}
			}
			usage := b.usage
			if usage == nil {
				b.failed = true
				if e := b.event("error", map[string]any{"error": map[string]string{"type": "api_error", "message": "usage_unavailable"}}); e != nil {
					return 0, e
				}
				continue
			}
			if e := b.event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": reason(b.finish), "stop_sequence": nil}, "usage": usage}); e != nil {
				return 0, e
			}
			if e := b.event("message_stop", map[string]any{}); e != nil {
				return 0, e
			}
			continue
		}
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			return 0, provider.ErrProtocol
		}
		if _, ok := row["error"]; ok {
			b.failed = true
			if e := b.event("error", map[string]any{"error": map[string]string{"type": "api_error", "message": safeError(row)}}); e != nil {
				return 0, e
			}
			continue
		}
		id, _ := row["id"].(string)
		if e := b.start(id); e != nil {
			return 0, e
		}
		if usage, ok := row["usage"].(map[string]any); ok {
			b.usage = anthropicUsage(usage)
		}
		choices, _ := row["choices"].([]any)
		for _, entry := range choices {
			choice := entry.(map[string]any)
			if f, ok := choice["finish_reason"].(string); ok {
				b.finish = f
			}
			delta, _ := choice["delta"].(map[string]any)
			if text, ok := delta["content"].(string); ok && text != "" {
				if b.textIndex == nil {
					idx := b.next
					b.next++
					b.textIndex = &idx
					b.open[idx] = true
					if e := b.event("content_block_start", map[string]any{"index": idx, "content_block": map[string]string{"type": "text", "text": ""}}); e != nil {
						return 0, e
					}
				}
				if e := b.event("content_block_delta", map[string]any{"index": *b.textIndex, "delta": map[string]string{"type": "text_delta", "text": text}}); e != nil {
					return 0, e
				}
			}
			if calls, ok := delta["tool_calls"].([]any); ok {
				for _, raw := range calls {
					call := raw.(map[string]any)
					index := int(call["index"].(float64))
					fn := call["function"].(map[string]any)
					idx, exists := b.blocks[index]
					if !exists {
						idx = b.next
						b.next++
						b.blocks[index] = idx
						b.open[idx] = true
						if e := b.event("content_block_start", map[string]any{"index": idx, "content_block": map[string]any{"type": "tool_use", "id": call["id"], "name": fn["name"], "input": map[string]any{}}}); e != nil {
							return 0, e
						}
					}
					if args, ok := fn["arguments"].(string); ok && args != "" {
						if e := b.event("content_block_delta", map[string]any{"index": idx, "delta": map[string]string{"type": "input_json_delta", "partial_json": args}}); e != nil {
							return 0, e
						}
					}
				}
			}
		}
	}
	return len(p), nil
}
func anthropicCompletion(row map[string]any) (map[string]any, error) {
	choices, ok := row["choices"].([]any)
	if !ok || len(choices) != 1 {
		return nil, provider.ErrProtocol
	}
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	content := []any{}
	if text, ok := msg["content"].(string); ok && text != "" {
		content = append(content, map[string]string{"type": "text", "text": text})
	}
	if calls, ok := msg["tool_calls"].([]any); ok {
		for _, raw := range calls {
			c := raw.(map[string]any)
			f := c["function"].(map[string]any)
			var args map[string]any
			if json.Unmarshal([]byte(f["arguments"].(string)), &args) != nil {
				return nil, provider.ErrProtocol
			}
			content = append(content, map[string]any{"type": "tool_use", "id": c["id"], "name": f["name"], "input": args})
		}
	}
	finish, _ := choice["finish_reason"].(string)
	u, ok := row["usage"].(map[string]any)
	if !ok {
		return nil, provider.ErrProtocol
	}
	usage := anthropicUsage(u)
	return map[string]any{"id": row["id"], "type": "message", "role": "assistant", "model": row["model"], "content": content, "stop_reason": reason(finish), "stop_sequence": nil, "usage": usage}, nil
}
