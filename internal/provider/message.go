package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/url"
	"strings"
)

const MaxChatBytes = 2 << 20
const MaxTextBytes = 64 << 10
const MaxInlineImageBytes = 256 << 10
const MaxImages = 3

// Admission only: never used to fabricate a charge or actual usage receipt.
const ImageInputBound = 16384

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	var content any = m.Content
	if len(m.ToolCalls) > 0 && m.Content == "" && m.Parts == nil {
		content = nil
	}
	if m.Parts != nil {
		if m.Content != "" {
			return nil, ErrProtocol
		}
		content = m.Parts
	}
	return json.Marshal(struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		Name       string     `json:"name,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	}{m.Role, content, m.Name, m.ToolCallID, m.ToolCalls})
}
func (m *Message) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		Name       string          `json:"name"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []ToolCall      `json:"tool_calls"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrProtocol
	}
	*m = Message{Role: wire.Role, Name: wire.Name, ToolCallID: wire.ToolCallID, ToolCalls: wire.ToolCalls}
	content := bytes.TrimSpace(wire.Content)
	if (len(content) == 0 || string(content) == "null") && wire.Role == "assistant" && len(wire.ToolCalls) > 0 {
		return nil
	}
	if len(content) == 0 {
		return ErrProtocol
	}
	if content[0] == '"' {
		return json.Unmarshal(content, &m.Content)
	}
	if content[0] != '[' {
		return ErrProtocol
	}
	d = json.NewDecoder(bytes.NewReader(content))
	d.DisallowUnknownFields()
	if d.Decode(&m.Parts) != nil || len(m.Parts) == 0 {
		return ErrProtocol
	}
	return nil
}
func (m Message) Text() string {
	if m.Parts == nil {
		return m.Content
	}
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
func HasImages(messages []Message) bool {
	for _, m := range messages {
		for _, p := range m.Parts {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}
func HasParts(messages []Message) bool {
	for _, m := range messages {
		if m.Parts != nil {
			return true
		}
	}
	return false
}

// InputBound counts text bytes, framing and a conservative image allowance.
// Inline bytes/URLs are not a tokenizer and never determine actual fees.
func InputBound(messages []Message) int {
	n := 64 + 32*len(messages)
	for _, m := range messages {
		n += len(m.Text())
		n += len(m.Name) + len(m.ToolCallID)
		for _, call := range m.ToolCalls {
			n += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments) + 32
		}
		for _, p := range m.Parts {
			if p.Type == "image_url" {
				n += ImageInputBound
			}
		}
	}
	return n
}
func ValidateMessages(messages []Message) error {
	if len(messages) == 0 || len(messages) > 128 {
		return ErrProtocol
	}
	images := 0
	pending := map[string]bool{}
	seen := map[string]bool{}
	for _, m := range messages {
		if m.Role != "system" && m.Role != "developer" && m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			return ErrProtocol
		}
		if m.Name != "" && !functionName.MatchString(m.Name) {
			return ErrProtocol
		}
		if m.Role == "tool" {
			if !pending[m.ToolCallID] || m.Parts != nil || len(m.ToolCalls) > 0 {
				return ErrProtocol
			}
			delete(pending, m.ToolCallID)
		} else {
			if len(pending) > 0 || m.ToolCallID != "" {
				return ErrProtocol
			}
			if len(m.ToolCalls) > 0 {
				if m.Role != "assistant" || m.Parts != nil || len(m.ToolCalls) > 16 {
					return ErrProtocol
				}
				for _, call := range m.ToolCalls {
					var args map[string]json.RawMessage
					if call.Type != "function" || !callID.MatchString(call.ID) || seen[call.ID] || !functionName.MatchString(call.Function.Name) || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
						return ErrProtocol
					}
					seen[call.ID] = true
					pending[call.ID] = true
				}
			}
		}
		if m.Parts == nil {
			continue
		}
		if m.Content != "" || len(m.Parts) == 0 || len(m.Parts) > 32 {
			return ErrProtocol
		}
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				if p.ImageURL != nil || p.Text == "" {
					return ErrProtocol
				}
			case "image_url":
				images++
				if m.Role != "user" || images > MaxImages || p.Text != "" || p.ImageURL == nil || !validImage(*p.ImageURL) {
					return ErrProtocol
				}
			default:
				return ErrProtocol
			}
		}
	}
	if len(pending) > 0 || InputBound(messages) > MaxTextBytes {
		return ErrProtocol
	}
	return nil
}
func validImage(v ImageURL) bool {
	if v.Detail != "" && v.Detail != "auto" && v.Detail != "low" && v.Detail != "high" {
		return false
	}
	if strings.HasPrefix(v.URL, "data:") {
		media, encoded, ok := strings.Cut(v.URL, ",")
		if !ok || (media != "data:image/png;base64" && media != "data:image/jpeg;base64") || len(encoded) > base64.StdEncoding.EncodedLen(MaxInlineImageBytes) {
			return false
		}
		raw, e := base64.StdEncoding.Strict().DecodeString(encoded)
		if e != nil || len(raw) > MaxInlineImageBytes || base64.StdEncoding.EncodeToString(raw) != encoded {
			return false
		}
		cfg, kind, e := image.DecodeConfig(bytes.NewReader(raw))
		return e == nil && cfg.Width > 0 && cfg.Height > 0 && cfg.Width <= 2048 && cfg.Height <= 2048 && ((kind == "png" && media == "data:image/png;base64") || (kind == "jpeg" && media == "data:image/jpeg;base64"))
	}
	u, e := url.Parse(v.URL)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(v.URL) > 4096 || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
		return false
	}
	// No download or DNS resolution here. Upstream fetch policy is independent.
	return !strings.ContainsAny(v.URL, "\r\n\t ")
}

var ErrUnsupportedInput = errors.New("provider input capability unavailable")

func Supports(p Provider, r ChatRequest) bool {
	if HasTools(r) {
		c, ok := p.(interface{ SupportsTools(string) bool })
		if !ok || !c.SupportsTools(r.Model) {
			return false
		}
	}
	if !HasImages(r.Messages) {
		return true
	}
	c, ok := p.(interface{ SupportsImages(string) bool })
	return ok && c.SupportsImages(r.Model)
}
