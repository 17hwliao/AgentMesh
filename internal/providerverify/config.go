// Package providerverify loads only the opt-in inputs for a controlled real
// Provider evidence run. It never constructs adapters or opens a connection.
package providerverify

import (
	"net/url"
	"strings"
)

const (
	ArkBaseURLEnvironment    = "ARK_BASE_URL"
	ArkModelEnvironment      = "ARK_MODEL"
	ArkAPIKeyEnvironment     = "ARK_API_KEY"
	OllamaBaseURLEnvironment = "OLLAMA_BASE_URL"
	OllamaModelEnvironment   = "OLLAMA_MODEL"

	CodeConfigurationMissing = "provider_configuration_missing"
	CodeConfigurationInvalid = "provider_configuration_invalid"
)

type Config struct {
	ArkBaseURL    string `json:"-"`
	ArkModel      string `json:"-"`
	ArkAPIKey     string `json:"-"`
	OllamaBaseURL string `json:"-"`
	OllamaModel   string `json:"-"`
}

type Report struct {
	Status           string `json:"status"`
	Code             string `json:"code,omitempty"`
	NetworkAttempts  int    `json:"network_attempts"`
	ProviderAttempts int    `json:"provider_attempts"`
}

func Load(lookup func(string) string) (Config, Report, error) {
	if lookup == nil {
		return unavailable(CodeConfigurationMissing)
	}
	config := Config{
		ArkBaseURL:    strings.TrimSpace(lookup(ArkBaseURLEnvironment)),
		ArkModel:      strings.TrimSpace(lookup(ArkModelEnvironment)),
		ArkAPIKey:     strings.TrimSpace(lookup(ArkAPIKeyEnvironment)),
		OllamaBaseURL: strings.TrimSpace(lookup(OllamaBaseURLEnvironment)),
		OllamaModel:   strings.TrimSpace(lookup(OllamaModelEnvironment)),
	}
	if config.ArkBaseURL == "" || config.ArkModel == "" || config.ArkAPIKey == "" || config.OllamaBaseURL == "" || config.OllamaModel == "" {
		return unavailable(CodeConfigurationMissing)
	}
	if !validBaseURL(config.ArkBaseURL) || !validBaseURL(config.OllamaBaseURL) {
		return unavailable(CodeConfigurationInvalid)
	}
	return config, Report{Status: "verification_pending", NetworkAttempts: 0, ProviderAttempts: 0}, nil
}

func unavailable(code string) (Config, Report, error) {
	return Config{}, Report{Status: "verification_unavailable", Code: code, NetworkAttempts: 0, ProviderAttempts: 0}, configurationError(code)
}

type configurationError string

func (e configurationError) Error() string { return string(e) }

func validBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}
