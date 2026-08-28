// Package gatewayclient contains the HTTP-only boundary used by local demo
// clients. It deliberately does not import routing or provider implementations.
package gatewayclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"agentmesh/internal/provider"
)

const chatPath = "/v1/chat/completions"

// ValidateEndpoint keeps the unauthenticated first-stage clients local.
func ValidateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil {
		return errors.New("endpoint must be an http://127.0.0.1 URL")
	}
	return nil
}

// Stream sends one authenticated stream=true chat request and writes only text
// deltas to out. The raw key is caller-owned and is never logged or retained.
func Stream(ctx context.Context, endpoint, apiKey, model string, messages []provider.Message, out io.Writer) error {
	if err := ValidateEndpoint(endpoint); err != nil {
		return err
	}
	if strings.TrimSpace(apiKey) == "" {
		return errors.New("api_key_missing")
	}
	body, err := json.Marshal(map[string]any{"model": model, "messages": messages, "stream": true})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+chatPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return readHTTPError(response)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return errors.New("gateway response is not SSE")
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1024), 128<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return nil
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("decode SSE event: %w", err)
		}
		if event.Error != nil {
			return fmt.Errorf("gateway stream error: %s", event.Error.Code)
		}
		for _, choice := range event.Choices {
			if _, err := io.WriteString(out, choice.Delta.Content); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func readHTTPError(response *http.Response) error {
	defer io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&payload); err == nil && payload.Error.Code != "" {
		return fmt.Errorf("gateway error: %s", payload.Error.Code)
	}
	return fmt.Errorf("gateway returned HTTP %d", response.StatusCode)
}
