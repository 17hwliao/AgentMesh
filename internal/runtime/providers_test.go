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
