package embedding

import (
	"errors"
	"strconv"
	"strings"
)

const (
	CodeConfigurationMissing = "embedding_provider_configuration_missing"
	CodeConfigurationInvalid = "embedding_provider_configuration_invalid"
)

type ConfigurationError struct{ Code string }

func (e *ConfigurationError) Error() string { return e.Code }

// Build constructs an explicit process-level embedding provider. Empty or
// "mock" selects the deterministic offline provider. Ollama is opt-in and
// refuses missing/invalid endpoint or model before any network request.
func Build(lookup func(string) string) ([]Provider, error) {
	selection := strings.ToLower(strings.TrimSpace(lookup("AGENTMESH_EMBEDDING_PROVIDER")))
	if selection == "" || selection == "mock" {
		dimension := DefaultDimension
		if raw := strings.TrimSpace(lookup("AGENTMESH_EMBEDDING_DIMENSION")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 || parsed > MaxDimension {
				return nil, &ConfigurationError{CodeConfigurationInvalid}
			}
			dimension = parsed
		}
		provider, err := NewMockProvider(dimension)
		if err != nil {
			return nil, &ConfigurationError{CodeConfigurationInvalid}
		}
		return []Provider{provider}, nil
	}
	if selection != "ollama" {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	baseURL := lookup("AGENTMESH_EMBEDDING_BASE_URL")
	model := lookup("AGENTMESH_EMBEDDING_MODEL")
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(model) == "" {
		return nil, &ConfigurationError{CodeConfigurationMissing}
	}
	provider, err := NewOllamaProvider(OllamaConfig{BaseURL: baseURL, Model: model})
	if err != nil {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	return []Provider{provider}, nil
}

func IsConfigurationError(err error) (string, bool) {
	var configuration *ConfigurationError
	if errors.As(err, &configuration) {
		return configuration.Code, true
	}
	return "", false
}
