// Package ollama adapts Ollama's native NDJSON /api/chat stream.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agentmesh/internal/provider"
)

type Config struct {
	BaseURL string
	Model   string
}

type Adapter struct {
	baseURL string
	model   string
	client  *http.Client
}

func New(config Config, client *http.Client) (*Adapter, error) {
	baseURL, err := validBaseURL(config.BaseURL)
	if err != nil || strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("invalid Ollama configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Adapter{baseURL: baseURL, model: config.Model, client: client}, nil
}

func (a *Adapter) Name() string { return "ollama" }

func (a *Adapter) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/api/tags", nil)
	if err != nil {
		return provider.ErrProtocol
	}
	response, err := a.client.Do(request)
	if err != nil {
		return provider.ErrUpstream
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return provider.ErrUpstream
	}
	return nil
}

func (a *Adapter) Stream(ctx context.Context, input provider.ChatRequest, emit provider.Emit) error {
	body, err := json.Marshal(map[string]any{"model": a.model, "messages": input.Messages, "stream": true})
	if err != nil {
		return provider.ErrProtocol
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return provider.ErrProtocol
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return provider.ErrUpstream
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return provider.ErrUpstream
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1024), 128<<10)
	for scanner.Scan() {
		var event struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil || event.Error != "" {
			return provider.ErrUpstream
		}
		if event.Message.Content != "" {
			if err := emit(provider.Chunk{Delta: event.Message.Content}); err != nil {
				return err
			}
		}
		if event.Done {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return provider.ErrUpstream
	}
	return provider.ErrProtocol
}

func validBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid base URL")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}
