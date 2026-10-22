package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolHistoryRequiresPairedResultsAndObjectArguments(t *testing.T) {
	call := ToolCall{ID: "call_1", Type: "function", Function: CallFunction{Name: "Read", Arguments: `{"path":"x"}`}}
	valid := []Message{{Role: "user", Content: "read"}, {Role: "assistant", ToolCalls: []ToolCall{call}}, {Role: "tool", ToolCallID: "call_1", Content: "result"}}
	if ValidateMessages(valid) != nil {
		t.Fatal("paired history rejected")
	}
	for name, messages := range map[string][]Message{
		"missing": valid[:2], "orphan": valid[2:], "wrong_id": {valid[0], valid[1], {Role: "tool", ToolCallID: "wrong", Content: "x"}},
		"interrupted": {valid[0], valid[1], {Role: "user", Content: "x"}}, "duplicate_result": append(append([]Message{}, valid...), valid[2]),
		"duplicate_call": {valid[0], valid[1], valid[2], valid[1], valid[2]},
	} {
		t.Run(name, func(t *testing.T) {
			if ValidateMessages(messages) == nil {
				t.Fatal("invalid history accepted")
			}
		})
	}
	for _, args := range []string{`null`, `[]`, `{`, `"x"`} {
		bad := call
		bad.Function.Arguments = args
		if ValidateMessages([]Message{{Role: "assistant", ToolCalls: []ToolCall{bad}}, valid[2]}) == nil {
			t.Fatal("non-object arguments accepted", args)
		}
	}
	raw, e := json.Marshal(valid[1])
	if e != nil || !strings.Contains(string(raw), `"content":null`) {
		t.Fatal(string(raw), e)
	}
	var round Message
	if json.Unmarshal(raw, &round) != nil || round.ToolCalls[0].Function.Arguments != call.Function.Arguments {
		t.Fatal("tool history lost on wire")
	}
}

func TestInterleavedToolFragmentsAndTerminalValidation(t *testing.T) {
	var stream ToolStream
	for _, ds := range [][]ToolCallDelta{
		{{Index: 0, ID: "one", Type: "function", Function: CallFunction{Name: "Read", Arguments: `{"path":`}}},
		{{Index: 1, ID: "two", Function: CallFunction{Name: "Read", Arguments: `{"path":"b"}`}}},
		{{Index: 0, Function: CallFunction{Arguments: `"a"}`}}},
	} {
		if stream.Add(ds) != nil {
			t.Fatal("valid fragment rejected")
		}
	}
	if stream.Complete() != nil || stream.Calls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatal("interleaved fragments corrupted")
	}
	for name, ds := range map[string][]ToolCallDelta{
		"orphan":     {{Index: 0, Function: CallFunction{Arguments: `{}`}}},
		"negative":   {{Index: -1, ID: "x", Function: CallFunction{Name: "Read", Arguments: `{}`}}},
		"gap":        {{Index: 1, ID: "x", Function: CallFunction{Name: "Read", Arguments: `{}`}}},
		"unfinished": {{Index: 0, ID: "x", Function: CallFunction{Name: "Read", Arguments: `{`}}},
		"oversize":   {{Index: 0, ID: "x", Function: CallFunction{Name: "Read", Arguments: strings.Repeat("x", 65537)}}},
	} {
		t.Run(name, func(t *testing.T) {
			var s ToolStream
			if s.Add(ds) == nil && s.Complete() == nil {
				t.Fatal("invalid stream accepted")
			}
		})
	}
}

func TestToolDefinitionsCountTowardAdmissionAndNeverMutateMessages(t *testing.T) {
	r := ChatRequest{Messages: []Message{{Role: "user", Content: "read"}}, Tools: []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "Read", Description: strings.Repeat("x", 2000), Parameters: json.RawMessage(`{"type":"object"}`)}}}}
	if ValidateChat(r) != nil {
		t.Fatal("valid tools rejected")
	}
	if InputBound(BudgetMessages(r)) < InputBound(r.Messages)+2000 || len(r.Messages) != 1 {
		t.Fatal("schema not budgeted or actual history mutated")
	}
	r.Tools = append(r.Tools, r.Tools[0])
	if ValidateChat(r) == nil {
		t.Fatal("duplicate tools accepted")
	}
	r.Tools = r.Tools[:1]
	r.Messages[0].Content = strings.Repeat("x", MaxTextBytes-1000)
	if ValidateChat(r) == nil {
		t.Fatal("schemas bypassed total input bound")
	}
}
