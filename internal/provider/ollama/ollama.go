// Package ollama adapts Ollama's native NDJSON /api/chat stream.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"agentmesh/internal/provider"
	"agentmesh/internal/providerconfig"
	"agentmesh/internal/telemetry"
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
	baseURL, err := providerconfig.BaseURL(config.BaseURL)
	if err != nil || !providerconfig.ModelValid(config.Model) {
		return nil, errors.New("invalid Ollama configuration")
	}
	if client == nil {
		// Local models can spend tens of seconds loading or evaluating a larger
		// evidence prompt. The gateway still propagates caller cancellation; this
		// is only the upstream safety ceiling, not a request that cannot cancel.
		client = &http.Client{Timeout: 90 * time.Second, Transport: telemetry.Transport{}}
	}
	return &Adapter{baseURL: baseURL, model: config.Model, client: client}, nil
}

func (a *Adapter) Name() string               { return "ollama" }
func (a *Adapter) BillingModel(string) string { return a.model }

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
	if provider.ValidateChat(input) != nil {
		return provider.ErrProtocol
	}
	if provider.HasImages(input.Messages) || provider.HasTools(input) || len(input.ResponseFormat) > 0 || input.ParallelToolCalls != nil || len(input.ToolChoice) > 0 {
		return provider.ErrUnsupportedInput
	}
	// Native Ollama's text adapter does not accept OpenAI content-part arrays.
	messages := append([]provider.Message(nil), input.Messages...)
	for i := range messages {
		messages[i].Content = messages[i].Text()
		messages[i].Parts = nil
	}
	limit := input.MaxTokens
	if limit <= 0 {
		limit = 1024
	}
	options := map[string]any{"num_predict": limit}
	if input.Temperature != nil {
		options["temperature"] = *input.Temperature
	}
	if input.TopP != nil {
		options["top_p"] = *input.TopP
	}
	if input.Seed != nil {
		options["seed"] = *input.Seed
	}
	if len(input.Stop) > 0 && string(input.Stop) != "null" {
		var stop any
		_ = json.Unmarshal(input.Stop, &stop)
		if text, ok := stop.(string); ok {
			stop = []string{text}
		}
		options["stop"] = stop
	}
	body, err := json.Marshal(map[string]any{"model": a.model, "messages": messages, "stream": true, "options": options})
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
			Done            bool   `json:"done"`
			Error           string `json:"error"`
			PromptEvalCount *int   `json:"prompt_eval_count"`
			EvalCount       *int   `json:"eval_count"`
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
			if event.PromptEvalCount != nil && event.EvalCount != nil {
				if *event.PromptEvalCount < 0 || *event.EvalCount < 0 {
					return provider.ErrProtocol
				}
				if err := emit(provider.Chunk{Usage: &provider.Usage{InputTokens: *event.PromptEvalCount, OutputTokens: *event.EvalCount}}); err != nil {
					return err
				}
			}
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
