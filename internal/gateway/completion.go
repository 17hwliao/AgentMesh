package gateway

import (
	"agentmesh/internal/provider"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type completionWriter struct {
	w               http.ResponseWriter
	stream, started bool
	includeUsage    bool
	model, id       string
	created         int64
	text            strings.Builder
	tools           provider.ToolStream
	usage           *provider.Usage
	finish          string
}

func newCompletion(w http.ResponseWriter, model string, stream bool) *completionWriter {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return &completionWriter{w: w, stream: stream, model: model, id: "chatcmpl-" + hex.EncodeToString(b[:]), created: time.Now().Unix(), finish: "stop", includeUsage: true}
}
func publicUsage(u *provider.Usage) any {
	if u == nil {
		return nil
	}
	// Legacy aliases keep the existing AgentMesh SDK consumers working while
	// standard clients use prompt/completion/total and cached-token details.
	return map[string]any{"prompt_tokens": u.InputTokens, "completion_tokens": u.OutputTokens, "total_tokens": u.InputTokens + u.OutputTokens, "prompt_tokens_details": map[string]int{"cached_tokens": u.CachedInputTokens}, "input_tokens": u.InputTokens, "output_tokens": u.OutputTokens, "estimated": u.Estimated}
}
func (c *completionWriter) frame(delta any, finish any, choices bool, usage any) error {
	rows := []any{}
	if choices {
		rows = append(rows, map[string]any{"index": 0, "delta": delta, "finish_reason": finish})
	}
	return writeSSE(c.w, map[string]any{"id": c.id, "object": "chat.completion.chunk", "created": c.created, "model": c.model, "choices": rows, "usage": usage})
}
func (c *completionWriter) start() error {
	if c.started {
		return nil
	}
	c.started = true
	c.w.Header().Set("Content-Type", "text/event-stream")
	c.w.Header().Set("Cache-Control", "no-store")
	return c.frame(map[string]string{"role": "assistant", "content": ""}, nil, true, nil)
}
func (c *completionWriter) emit(p provider.Chunk) error {
	if p.FinishReason != "" {
		switch p.FinishReason {
		case "stop", "length", "tool_calls", "content_filter":
			c.finish = p.FinishReason
		default:
			return provider.ErrProtocol
		}
	}
	if p.Usage != nil {
		u := *p.Usage
		c.usage = &u
	}
	if c.text.Len()+len(p.Delta)+c.tools.Bytes > 1<<20 {
		return provider.ErrProtocol
	}
	c.text.WriteString(p.Delta)
	if len(p.ToolCalls) > 0 {
		if c.tools.Add(p.ToolCalls) != nil {
			return provider.ErrProtocol
		}
		c.finish = "tool_calls"
	}
	if !c.stream || (p.Delta == "" && len(p.ToolCalls) == 0) {
		return nil
	}
	if e := c.start(); e != nil {
		return e
	}
	delta := map[string]any{}
	if p.Delta != "" {
		delta["content"] = p.Delta
	}
	if len(p.ToolCalls) > 0 {
		delta["tool_calls"] = p.ToolCalls
	}
	return c.frame(delta, nil, true, nil)
}
func (c *completionWriter) complete() error {
	if c.tools.Complete() != nil {
		return provider.ErrProtocol
	}
	if c.stream {
		if e := c.start(); e != nil {
			return e
		}
		if e := c.frame(map[string]any{}, c.finish, true, nil); e != nil {
			return e
		}
		if c.usage != nil && c.includeUsage {
			if e := c.frame(nil, nil, false, publicUsage(c.usage)); e != nil {
				return e
			}
		}
		return writeDone(c.w)
	}
	var content any = c.text.String()
	var calls []provider.ToolCall
	for i := 0; i < len(c.tools.Calls); i++ {
		calls = append(calls, c.tools.Calls[i])
	}
	if content == "" && len(calls) > 0 {
		content = nil
	}
	message := map[string]any{"role": "assistant", "content": content}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	c.w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(c.w).Encode(map[string]any{"id": c.id, "object": "chat.completion", "created": c.created, "model": c.model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": c.finish}}, "usage": publicUsage(c.usage)})
}
func (c *completionWriter) fail(code string) {
	if c.started {
		_ = writeSSE(c.w, map[string]any{"error": map[string]string{"code": code, "type": "api_error", "message": code}})
		_ = writeDone(c.w)
	} else {
		writeJSONError(c.w, http.StatusServiceUnavailable, code)
	}
}
