package providerverify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsMissingConfigurationWithoutNetworkEvidence(t *testing.T) {
	_, report, err := Load(func(string) string { return "" })
	if err == nil || report.Status != "verification_unavailable" || report.Code != CodeConfigurationMissing || report.NetworkAttempts != 0 || report.ProviderAttempts != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestLoadRequiresOnlySelectedProviderConfiguration(t *testing.T) {
	for _, test := range []struct {
		name      string
		providers string
		values    map[string]string
		want      []string
	}{
		{name: "ollama only", providers: "ollama", values: map[string]string{OllamaBaseURLEnvironment: "http://127.0.0.1:11434", OllamaModelEnvironment: "qwen2.5:7b"}, want: []string{"ollama"}},
		{name: "ark only", providers: "ark", values: map[string]string{ArkBaseURLEnvironment: "https://ark.example.test/v1", ArkModelEnvironment: "ark-model", ArkAPIKeyEnvironment: "secret"}, want: []string{"ark"}},
		{name: "both", providers: "ark,ollama", values: map[string]string{ArkBaseURLEnvironment: "https://ark.example.test/v1", ArkModelEnvironment: "ark-model", ArkAPIKeyEnvironment: "secret", OllamaBaseURLEnvironment: "http://127.0.0.1:11434", OllamaModelEnvironment: "qwen2.5:7b"}, want: []string{"ark", "ollama"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, report, err := Load(func(name string) string {
				if name == ProvidersEnvironment {
					return test.providers
				}
				return test.values[name]
			})
			if err != nil || report.Status != "verification_pending" || strings.Join(config.Providers, ",") != strings.Join(test.want, ",") {
				t.Fatalf("config=%+v report=%+v err=%v", config, report, err)
			}
		})
	}
}

func TestLoadRejectsMissingOrInvalidProviderSelectionBeforeNetworkEvidence(t *testing.T) {
	for _, providers := range []string{"", "ollama,ark", "mock"} {
		_, report, err := Load(func(name string) string {
			if name == ProvidersEnvironment {
				return providers
			}
			return "configured"
		})
		if err == nil || report.Status != "verification_unavailable" || report.NetworkAttempts != 0 || report.ProviderAttempts != 0 {
			t.Fatalf("providers=%q report=%+v err=%v", providers, report, err)
		}
	}
}

func TestLoadAcceptsCompleteConfigurationButNeverSerializesIt(t *testing.T) {
	values := map[string]string{
		ProvidersEnvironment:     "ark,ollama",
		ArkBaseURLEnvironment:    "https://ark.example.test/v1",
		ArkModelEnvironment:      "ark-model",
		ArkAPIKeyEnvironment:     "secret-ark-key",
		OllamaBaseURLEnvironment: "http://127.0.0.1:11434",
		OllamaModelEnvironment:   "ollama-model",
	}
	config, report, err := Load(func(name string) string { return values[name] })
	if err != nil || config.ArkAPIKey == "" || report.Status != "verification_pending" || report.NetworkAttempts != 0 {
		t.Fatalf("config=%+v report=%+v err=%v", config, report, err)
	}
	encoded, err := json.Marshal(struct {
		Config Config `json:"config"`
		Report Report `json:"report"`
	}{config, report})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("safe JSON leaked configured value: %q", encoded)
		}
	}
}

func TestLoadRejectsInvalidURLs(t *testing.T) {
	_, report, err := Load(func(name string) string {
		if name == ProvidersEnvironment {
			return "ark"
		}
		if name == ArkBaseURLEnvironment {
			return "https://ark.example.test/?token=not-allowed"
		}
		return "value"
	})
	if err == nil || report.Code != CodeConfigurationInvalid || report.NetworkAttempts != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestVerificationScriptKeepsSingleRequestAndFinallyCleanup(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "scripts", "verify-real-provider.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{"try {", "finally {", "2>$null", "127.0.0.1:18083", "AGENTMESH_REAL_PROVIDERS", "--providers', $providerSelection", "provider-evidence", "Remove-Item", "network_attempts"} {
		if !strings.Contains(text, required) {
			t.Fatalf("script missing %q", required)
		}
	}
	if strings.Count(text, "/v1/chat/completions") != 1 || strings.Contains(text, "Write-Output $temporaryKey") {
		t.Fatalf("script must make one request without key output")
	}
}
