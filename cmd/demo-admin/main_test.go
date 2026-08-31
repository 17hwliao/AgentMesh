package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunShowsLifecycleWithoutLeakingKeys(t *testing.T) {
	var output bytes.Buffer
	if err := run(&output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{
		"create_tenant_and_key=201",
		"chat_with_new_key=200",
		"revoke_key=204",
		"chat_with_revoked_key=401 code=auth_failed",
		"chat_with_replacement_key=200",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in %q", expected, text)
		}
	}
	if strings.Contains(text, "api_key") || strings.Contains(text, "Bearer ") {
		t.Fatalf("sensitive demo output: %q", text)
	}
}
