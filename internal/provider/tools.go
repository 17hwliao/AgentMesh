package provider

import (
	"encoding/json"
	"regexp"
	"strings"
)

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict,omitempty"`
}
type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}
type CallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function CallFunction `json:"function"`
}
type ToolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function CallFunction `json:"function"`
}

var functionName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
var callID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func HasTools(r ChatRequest) bool {
	if len(r.Tools) > 0 {
		return true
	}
	for _, m := range r.Messages {
		if len(m.ToolCalls) > 0 || m.Role == "tool" {
			return true
		}
	}
	return false
}
func ValidateChat(r ChatRequest) error {
	if ValidateMessages(r.Messages) != nil || len(r.Tools) > 64 {
		return ErrProtocol
	}
	names := map[string]bool{}
	for _, t := range r.Tools {
		var schema map[string]json.RawMessage
		if t.Type != "function" || !functionName.MatchString(t.Function.Name) || names[t.Function.Name] || len(t.Function.Description) > 8192 || len(t.Function.Parameters) > 32<<10 || json.Unmarshal(t.Function.Parameters, &schema) != nil || string(schema["type"]) != `"object"` {
			return ErrProtocol
		}
		names[t.Function.Name] = true
	}
	if len(r.ToolChoice) > 0 && string(r.ToolChoice) != "null" {
		if len(names) == 0 {
			return ErrProtocol
		}
		var value string
		if json.Unmarshal(r.ToolChoice, &value) == nil {
			if value != "auto" && value != "none" && value != "required" {
				return ErrProtocol
			}
		} else {
			var choice struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(r.ToolChoice, &choice) != nil || choice.Type != "function" || !names[choice.Function.Name] {
				return ErrProtocol
			}
		}
	}
	if r.TopP != nil && (*r.TopP < 0 || *r.TopP > 1) {
		return ErrProtocol
	}
	if len(r.Stop) > 0 && string(r.Stop) != "null" {
		var one string
		var many []string
		if json.Unmarshal(r.Stop, &one) == nil {
			many = []string{one}
		} else if json.Unmarshal(r.Stop, &many) != nil {
			return ErrProtocol
		}
		if len(many) > 4 {
			return ErrProtocol
		}
		for _, s := range many {
			if len(s) > 1024 {
				return ErrProtocol
			}
		}
	}
	if len(r.ResponseFormat) > 0 && string(r.ResponseFormat) != "null" {
		var f struct {
			Type   string          `json:"type"`
			Schema json.RawMessage `json:"json_schema"`
		}
		if json.Unmarshal(r.ResponseFormat, &f) != nil || (f.Type != "text" && f.Type != "json_object" && f.Type != "json_schema") || (f.Type == "json_schema" && len(f.Schema) == 0) {
			return ErrProtocol
		}
	}
	if InputBound(BudgetMessages(r)) > MaxTextBytes {
		return ErrProtocol
	}
	return nil
}

// Metadata is only copied into the private admission input, never into the
// upstream messages. This keeps tool/schema costs inside existing quota gates.
func BudgetMessages(r ChatRequest) []Message {
	if len(r.Tools) == 0 && len(r.ResponseFormat) == 0 {
		return r.Messages
	}
	raw, _ := json.Marshal(struct {
		Tools  []ToolDefinition
		Format json.RawMessage
	}{r.Tools, r.ResponseFormat})
	return append(append([]Message(nil), r.Messages...), Message{Role: "system", Content: string(raw)})
}
func (c Chunk) OutputText() string {
	var b strings.Builder
	b.WriteString(c.Delta)
	for _, t := range c.ToolCalls {
		b.WriteString(t.Function.Name)
		b.WriteString(t.Function.Arguments)
	}
	return b.String()
}

// Tool arguments remain fragments while streaming. Only terminal validation
// parses the accumulated object; no tool is ever executed by the gateway.
type ToolStream struct {
	Calls map[int]ToolCall
	Bytes int
}

func (t *ToolStream) Add(ds []ToolCallDelta) error {
	if t.Calls == nil {
		t.Calls = map[int]ToolCall{}
	}
	for _, d := range ds {
		if d.Index < 0 || d.Index >= 16 || (d.Type != "" && d.Type != "function") {
			return ErrProtocol
		}
		c := t.Calls[d.Index]
		if c.ID == "" && (d.ID == "" || d.Function.Name == "") {
			return ErrProtocol
		}
		if d.ID != "" {
			if c.ID != "" && c.ID != d.ID {
				return ErrProtocol
			}
			c.ID = d.ID
		}
		if d.Function.Name != "" {
			if c.Function.Name != "" && c.Function.Name != d.Function.Name {
				return ErrProtocol
			}
			c.Function.Name = d.Function.Name
		}
		c.Type = "function"
		c.Function.Arguments += d.Function.Arguments
		t.Bytes += len(d.ID) + len(d.Function.Name) + len(d.Function.Arguments)
		if t.Bytes > 64<<10 {
			return ErrProtocol
		}
		t.Calls[d.Index] = c
	}
	return nil
}
func (t *ToolStream) Complete() error {
	ids := map[string]bool{}
	for i := 0; i < len(t.Calls); i++ {
		c, ok := t.Calls[i]
		var args map[string]json.RawMessage
		if !ok || !callID.MatchString(c.ID) || ids[c.ID] || !functionName.MatchString(c.Function.Name) || json.Unmarshal([]byte(c.Function.Arguments), &args) != nil || args == nil {
			return ErrProtocol
		}
		ids[c.ID] = true
	}
	return nil
}
