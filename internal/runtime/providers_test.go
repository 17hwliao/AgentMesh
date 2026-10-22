package runtime

import (
	"strings"
	"testing"

	"agentmesh/internal/provider"
)

func TestBuildDefaultsToMockWithoutReadingEnvironment(t *testing.T) {
	lookups := 0
	providers, err := Build("", func(string) string { lookups++; return "" })
	if err != nil {
		t.Fatalf("Build() error=%v", err)
	}
	if lookups != 0 || len(providers) != 2 || providers[0].Name() != "mock-primary" || providers[1].Name() != "mock-fallback" {
		t.Fatalf("providers=%v lookups=%d", providerNames(providers), lookups)
	}
}

func TestBuildRejectsMissingRealConfigurationWithStableCode(t *testing.T) {
	_, err := Build("ark,ollama", func(string) string { return "" })
	code, ok := IsConfigurationError(err)
	if !ok || code != CodeProviderConfigurationMissing || strings.Contains(err.Error(), "ARK_") {
		t.Fatalf("Build() error=%v code=%q ok=%t", err, code, ok)
	}
}

func TestToolAliasesRejectUnboundOrMalformedDeclarationsBeforeRemoteDial(t *testing.T) {
	for _, raw := range []string{`["m"]`, `["unknown"]`, `["m","m"]`, `null`, `{}`, `[]`} {
		values := map[string]string{"COMPATIBLE_BASE_URL": "https://example.com/v1", "COMPATIBLE_API_KEY": "fixture", "COMPATIBLE_MODELS": `{"m":"qwen"}`, "COMPATIBLE_TOOL_MODELS": raw}
		ps, e := Build("compatible", func(k string) string { return values[k] })
		if raw == `["m"]` {
			if e != nil || !ps[0].(interface{ SupportsTools(string) bool }).SupportsTools("m") {
				t.Fatal("tool capability not bound", e)
			}
		} else {
			if e == nil {
				t.Fatal("invalid declaration accepted", raw)
			}
			values["AGENTMESH_RUNNER_GRPC_TARGET"] = "unused:50051"
			lookups := 0
			_, e = Build("compatible", func(k string) string {
				if strings.HasPrefix(k, "AGENTMESH_GRPC") {
					lookups++
				}
				return values[k]
			})
			if e == nil || lookups != 0 {
				t.Fatal("invalid declaration reached remote TLS/dial", raw, e)
			}
		}
	}
}

func TestBuildRejectsInvalidOrDuplicateOrder(t *testing.T) {
	for _, order := range []string{"mock,ark", "ark,ark", "unknown"} {
		_, err := Build(order, func(string) string { return "x" })
		code, ok := IsConfigurationError(err)
		if !ok || code != CodeProviderSelectionInvalid {
			t.Fatalf("order=%q error=%v code=%q ok=%t", order, err, code, ok)
		}
	}
}

func providerNames(providers []provider.Provider) []string {
	names := make([]string, 0, len(providers))
	for _, candidate := range providers {
		names = append(names, candidate.Name())
	}
	return names
}

func TestVisionAliasesAreExplicitAndBound(t *testing.T) {
	for _, raw := range []string{`["v"]`, `["unknown"]`, `["v","v"]`, `null`, `{}`, `[]`} {
		values := map[string]string{"COMPATIBLE_BASE_URL": "https://example.com/v1", "COMPATIBLE_API_KEY": "fixture", "COMPATIBLE_MODELS": `{"v":"vision-model"}`, "COMPATIBLE_VISION_MODELS": raw}
		ps, e := Build("compatible", func(k string) string { return values[k] })
		if raw == `["v"]` {
			if e != nil || !ps[0].(interface{ SupportsImages(string) bool }).SupportsImages("v") {
				t.Fatal("valid vision alias rejected")
			}
		} else if e == nil {
			t.Fatal("unbound/invalid vision declaration accepted", raw)
		}
	}
}

func TestBuildRejectsMisspelledCompatibleControls(t *testing.T) {
	for _, flag := range []string{"COMPATIBLE_DISABLE_THINKING", "COMPATIBLE_USE_MAX_COMPLETION_TOKENS"} {
		_, err := Build("compatible", func(name string) string {
			if name == flag {
				return "treu"
			}
			return ""
		})
		code, ok := IsConfigurationError(err)
		if !ok || code != CodeProviderConfigurationInvalid {
			t.Fatalf("flag=%s error=%v", flag, err)
		}
	}
}

func TestLocalAndRemoteRejectTheSameInvalidBindingsBeforeDial(t *testing.T) {
	for _, raw := range []string{`{"m":"a","m":"b"}`, `{"bad alias":"qwen-plus"}`, `{"m":" bad"}`, `{}`} {
		for _, remote := range []bool{false, true} {
			lookups := 0
			_, err := Build("compatible", func(name string) string {
				if name == "COMPATIBLE_MODELS" {
					return raw
				}
				if strings.HasPrefix(name, "AGENTMESH_GRPC") {
					lookups++
				}
				if name == "AGENTMESH_RUNNER_GRPC_TARGET" && remote {
					return "unused:50051"
				}
				return ""
			})
			code, ok := IsConfigurationError(err)
			if !ok || code != CodeProviderConfigurationInvalid || lookups != 0 {
				t.Fatalf("remote=%v err=%v TLS lookups=%d", remote, err, lookups)
			}
		}
	}
}
