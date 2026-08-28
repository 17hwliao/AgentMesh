// Package runtime builds the explicit, process-local Provider route used by
// cmd/api. It never logs or serializes environment values.
package runtime

import (
	"errors"
	"strings"

	"agentmesh/internal/provider"
	"agentmesh/internal/provider/ark"
	"agentmesh/internal/provider/ollama"
)

const (
	CodeProviderSelectionInvalid     = "provider_selection_invalid"
	CodeProviderConfigurationMissing = "provider_configuration_missing"
	CodeProviderConfigurationInvalid = "provider_configuration_invalid"
)

// ConfigurationError is safe to return to an operator: it contains only a
// stable code and never a variable name, endpoint, model, or credential.
type ConfigurationError struct{ Code string }

func (e *ConfigurationError) Error() string { return e.Code }

// Build returns mock by default. A real route is opt-in and cannot mix mock
// with real providers, preventing accidental network attempts in local demos.
func Build(orderRaw string, lookup func(string) string) ([]provider.Provider, error) {
	order, err := parseOrder(orderRaw)
	if err != nil {
		return nil, err
	}
	if len(order) == 1 && order[0] == "mock" {
		primary := provider.NewMock(provider.MockConfig{Name: "mock-primary", FailBeforeFirst: true, FailAfterChunks: -1})
		fallback := provider.NewMock(provider.MockConfig{Name: "mock-fallback", Chunks: []string{"mock response"}, FailAfterChunks: -1})
		return []provider.Provider{primary, fallback}, nil
	}

	providers := make([]provider.Provider, 0, len(order))
	for _, name := range order {
		switch name {
		case "ark":
			adapter, err := ark.New(ark.Config{BaseURL: lookup("ARK_BASE_URL"), Model: lookup("ARK_MODEL"), APIKey: lookup("ARK_API_KEY")}, nil)
			if err != nil {
				return nil, configurationError(lookup("ARK_BASE_URL"), lookup("ARK_MODEL"), lookup("ARK_API_KEY"))
			}
			providers = append(providers, adapter)
		case "ollama":
			adapter, err := ollama.New(ollama.Config{BaseURL: lookup("OLLAMA_BASE_URL"), Model: lookup("OLLAMA_MODEL")}, nil)
			if err != nil {
				return nil, configurationError(lookup("OLLAMA_BASE_URL"), lookup("OLLAMA_MODEL"))
			}
			providers = append(providers, adapter)
		}
	}
	return providers, nil
}

func parseOrder(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "mock" {
		return []string{"mock"}, nil
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	for index, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" || name == "mock" || (name != "ark" && name != "ollama") {
			return nil, &ConfigurationError{Code: CodeProviderSelectionInvalid}
		}
		if _, exists := seen[name]; exists {
			return nil, &ConfigurationError{Code: CodeProviderSelectionInvalid}
		}
		seen[name] = struct{}{}
		parts[index] = name
	}
	return parts, nil
}

func configurationError(values ...string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return &ConfigurationError{Code: CodeProviderConfigurationMissing}
		}
	}
	return &ConfigurationError{Code: CodeProviderConfigurationInvalid}
}

func IsConfigurationError(err error) (string, bool) {
	var configuration *ConfigurationError
	if errors.As(err, &configuration) {
		return configuration.Code, true
	}
	return "", false
}
