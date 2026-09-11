package raganswer

import (
	"strconv"
	"strings"
)

const (
	CodeConfigurationMissing = "rag_answer_provider_configuration_missing"
	CodeConfigurationInvalid = "rag_answer_provider_configuration_invalid"
)

type ConfigurationError struct{ Code string }

func (e *ConfigurationError) Error() string { return e.Code }

// Build defaults to an offline fake. Ollama is a separately opt-in generation
// path: missing endpoint/model is rejected before an upstream request.
func Build(lookup func(string) string) ([]Provider, error) {
	selection := strings.ToLower(strings.TrimSpace(lookup("AGENTMESH_RAG_ANSWER_PROVIDER")))
	if selection == "" || selection == "fake" {
		return []Provider{FakeProvider{}}, nil
	}
	if selection != "ollama" {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	baseURL, model := lookup("AGENTMESH_RAG_ANSWER_BASE_URL"), lookup("AGENTMESH_RAG_ANSWER_MODEL")
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(model) == "" {
		return nil, &ConfigurationError{CodeConfigurationMissing}
	}
	timeout := 20
	if value := strings.TrimSpace(lookup("AGENTMESH_RAG_ANSWER_TIMEOUT_SECONDS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 300 {
			return nil, &ConfigurationError{CodeConfigurationInvalid}
		}
		timeout = parsed
	}
	maxOutputTokens := 256
	if value := strings.TrimSpace(lookup("AGENTMESH_RAG_ANSWER_MAX_OUTPUT_TOKENS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 16 || parsed > 1024 {
			return nil, &ConfigurationError{CodeConfigurationInvalid}
		}
		maxOutputTokens = parsed
	}
	provider, err := NewOllamaProvider(OllamaConfig{BaseURL: baseURL, Model: model, TimeoutSeconds: timeout, MaxOutputTokens: maxOutputTokens})
	if err != nil {
		return nil, &ConfigurationError{CodeConfigurationInvalid}
	}
	return []Provider{provider}, nil
}

func IsConfigurationError(err error) (string, bool) {
	if value, ok := err.(*ConfigurationError); ok {
		return value.Code, true
	}
	return "", false
}
