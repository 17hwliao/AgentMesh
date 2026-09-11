package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type OllamaConfig struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

type OllamaProvider struct {
	baseURL string
	model   string
	client  *http.Client
}

func NewOllamaProvider(config OllamaConfig) (*OllamaProvider, error) {
	baseURL, err := validBaseURL(config.BaseURL)
	if err != nil || strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("%w: Ollama endpoint and model are required", ErrUnavailable)
	}
	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &OllamaProvider{baseURL: baseURL, model: config.Model, client: client}, nil
}

func (p *OllamaProvider) Name() string { return "ollama" }

func (p *OllamaProvider) Embed(ctx context.Context, _ string, inputs []string) ([][]float64, error) {
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{p.model, inputs})
	if err != nil {
		return nil, ErrProtocol
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, ErrProtocol
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: HTTP %s", ErrUpstream, response.Status)
	}
	var decoded struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	if len(decoded.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("%w: embedding count mismatch", ErrProtocol)
	}
	if err := validateVectors(decoded.Embeddings); err != nil {
		return nil, err
	}
	return decoded.Embeddings, nil
}

func validateVectors(vectors [][]float64) error {
	if len(vectors) == 0 {
		return fmt.Errorf("%w: empty embedding response", ErrProtocol)
	}
	dimension := len(vectors[0])
	if dimension == 0 || dimension > MaxDimension {
		return fmt.Errorf("%w: invalid embedding dimension", ErrProtocol)
	}
	for _, vector := range vectors {
		if len(vector) != dimension {
			return fmt.Errorf("%w: inconsistent embedding dimensions", ErrProtocol)
		}
	}
	return nil
}

func validBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid base URL")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}
