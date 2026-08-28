// Package ark adapts the Ark OpenAI-compatible chat-completions stream.
package ark

import (
	"bufio"
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
	APIKey  string
}

type Adapter struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

func New(config Config, client *http.Client) (*Adapter, error) {
	baseURL, err := validBaseURL(config.BaseURL)
	if err != nil || strings.TrimSpace(config.Model) == "" || strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("invalid Ark configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Adapter{baseURL: baseURL, model: config.Model, apiKey: config.APIKey, client: client}, nil
}

func (a *Adapter) Name() string { return "ark" }

func (a *Adapter) Health(ctx context.Context) error {
	request, err := a.request(ctx, http.MethodGet, "/models", nil)
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
	request, err := a.request(ctx, http.MethodPost, "/chat/completions", body)
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
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			return nil
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil || len(event.Error) > 0 {
			return provider.ErrUpstream
		}
		for _, choice := range event.Choices {
			if err := emit(provider.Chunk{Delta: choice.Delta.Content}); err != nil {
				return err
			}
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

func (a *Adapter) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	return request, nil
}

func validBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid base URL")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}
