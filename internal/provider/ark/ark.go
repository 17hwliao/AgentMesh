// Package ark adapts the Ark OpenAI-compatible chat-completions stream.
package ark

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"agentmesh/internal/provider"
	"agentmesh/internal/providerconfig"
	"agentmesh/internal/sse"
	"agentmesh/internal/telemetry"
)

type Config struct {
	BaseURL                string
	Model                  string
	APIKey                 string
	Name                   string
	Models                 map[string]string
	DisableThinking        bool
	UseMaxCompletionTokens bool
}

type Adapter struct {
	baseURL                string
	model                  string
	apiKey                 string
	client                 *http.Client
	name                   string
	models                 map[string]string
	disableThinking        bool
	useMaxCompletionTokens bool
}

func New(config Config, client *http.Client) (*Adapter, error) {
	baseURL, err := providerconfig.BaseURL(config.BaseURL)
	if err != nil || (len(config.Models) == 0 && !providerconfig.ModelValid(config.Model)) || !providerconfig.KeyValid(config.APIKey) || len(config.Models) > 32 {
		return nil, errors.New("invalid Ark configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, Transport: telemetry.Transport{}}
	}
	name := config.Name
	if name == "" {
		name = "ark"
	}
	if name != "ark" && name != "compatible" {
		return nil, provider.ErrProtocol
	}
	models := map[string]string{}
	for alias, actual := range config.Models {
		if !providerconfig.AliasValid(alias) || !providerconfig.ModelValid(actual) {
			return nil, provider.ErrProtocol
		}
		models[alias] = actual
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Adapter{baseURL: baseURL, model: config.Model, apiKey: config.APIKey, client: &copyClient, name: name, models: models, disableThinking: config.DisableThinking, useMaxCompletionTokens: config.UseMaxCompletionTokens}, nil
}

func (a *Adapter) Name() string { return a.name }
func (a *Adapter) BillingModel(alias string) string {
	if len(a.models) > 0 {
		return a.models[alias]
	}
	return a.model
}

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
	limit := input.MaxTokens
	if limit <= 0 {
		limit = 1024
	}
	model := a.model
	if len(a.models) > 0 {
		var ok bool
		model, ok = a.models[input.Model]
		if !ok {
			return provider.ErrProtocol
		}
	}
	payload := map[string]any{"model": model, "messages": input.Messages, "stream": true, "max_tokens": limit, "stream_options": map[string]bool{"include_usage": true}}
	// Qwen's newer models bound reasoning and answer tokens together with this
	// field. Keep legacy max_tokens for models that do not support it.
	if a.useMaxCompletionTokens {
		delete(payload, "max_tokens")
		payload["max_completion_tokens"] = limit
	}
	if a.disableThinking {
		payload["enable_thinking"] = false
	}
	if input.Temperature != nil {
		payload["temperature"] = *input.Temperature
	}
	body, err := json.Marshal(payload)
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

	reader := sse.NewReader(response.Body)
	usageSeen := false
	for {
		frame, err := reader.Next()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, sse.ErrLimit) {
				return provider.ErrProtocol
			}
			return provider.ErrUpstream
		}
		if frame.Type == "error" {
			return provider.ErrUpstream
		}
		payload := strings.TrimSpace(frame.Data)
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
			Usage *struct {
				PromptTokens     *int `json:"prompt_tokens"`
				CompletionTokens *int `json:"completion_tokens"`
				CachedTokens     int  `json:"prompt_cache_hit_tokens"`
				PromptDetails    struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil || (len(event.Error) > 0 && string(event.Error) != "null") {
			return provider.ErrUpstream
		}
		for _, choice := range event.Choices {
			// Role, finish and reasoning-only events are not answer chunks. In
			// particular, do not commit an empty response before the first answer.
			if choice.Delta.Content == "" {
				continue
			}
			if err := emit(provider.Chunk{Delta: choice.Delta.Content}); err != nil {
				return err
			}
		}
		if event.Usage != nil {
			if usageSeen || event.Usage.PromptTokens == nil || event.Usage.CompletionTokens == nil {
				return provider.ErrProtocol
			}
			usageSeen = true
			cached := event.Usage.CachedTokens
			if event.Usage.PromptDetails.CachedTokens > cached {
				cached = event.Usage.PromptDetails.CachedTokens
			}
			if *event.Usage.PromptTokens < 0 || *event.Usage.CompletionTokens < 0 || cached < 0 || cached > *event.Usage.PromptTokens {
				return provider.ErrProtocol
			}
			if err := emit(provider.Chunk{Usage: &provider.Usage{InputTokens: *event.Usage.PromptTokens, OutputTokens: *event.Usage.CompletionTokens, CachedInputTokens: cached}}); err != nil {
				return err
			}
		}
	}
}

func (a *Adapter) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	return request, nil
}
