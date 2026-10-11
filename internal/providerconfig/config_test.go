package providerconfig

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestModelMapRejectsAmbiguousBindings(t *testing.T) {
	for _, raw := range []string{`{"m":"first","m":"second"}`, `{" m":"model"}`, `{"m":"model name"}`, `{"m":null}`, `{"m":"model"} {}`, `[]`, `{}`} {
		if _, err := Models(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted ambiguous binding %s: %v", raw, err)
		}
	}
	models, err := Models(`{"sql-review":"qwen-plus","embedding":"nomic-embed-text:latest"}`)
	if err != nil || len(models) != 2 {
		t.Fatal(models, err)
	}
	large := map[string]string{}
	for i := 0; i < 33; i++ {
		large[strings.Repeat("x", i+1)] = "model"
	}
	raw, _ := json.Marshal(large)
	if _, err = Models(string(raw)); err == nil {
		t.Fatal("unbounded aliases")
	}
}

func TestEndpointAndModelCapabilities(t *testing.T) {
	for _, raw := range []string{"https://host/?key=secret", "https://user:secret@host/v1", "https://host:70000/v1", "file:///tmp/model", "https://host/v1?"} {
		if _, err := BaseURL(raw); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	values := map[string]string{"COMPATIBLE_MODELS": `{"m":"qwen-plus"}`, "COMPATIBLE_DISABLE_THINKING": "true", "COMPATIBLE_USE_MAX_COMPLETION_TOKENS": "true"}
	bindings, err := Bindings([]string{"compatible"}, func(name string) string { return values[name] })
	if err != nil || !bindings[0].DisableThinking || !bindings[0].UseMaxCompletionTokens {
		t.Fatal(bindings, err)
	}
}

func TestCredentialHeaderBounds(t *testing.T) {
	for i, key := range []string{"", "has space", "vertical\vtab", "form\ffeed", "control\x1f", "delete\x7f", "unicode-中文", strings.Repeat("a", 16385)} {
		if KeyValid(key) {
			t.Fatalf("invalid header accepted at case %d", i)
		}
	}
	if !KeyValid("fixture-key_1.2-3") {
		t.Fatal("printable credential rejected")
	}
}
