package mockopenai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestModels(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	response, body := doRequest(t, server, http.MethodGet, "/models", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("content type = %q", response.Header.Get("Content-Type"))
	}
	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if list.Object != "list" || len(list.Data) != 2 {
		t.Fatalf("unexpected model list: %+v", list)
	}
	if list.Data[0].ID != SmallModel || list.Data[1].ID != VisionModel {
		t.Fatalf("model ids = %q, %q", list.Data[0].ID, list.Data[1].ID)
	}
	for _, model := range list.Data {
		if model.Object != "model" || model.OwnedBy != "mockopenai" {
			t.Fatalf("unexpected model metadata: %+v", model)
		}
	}
}

func TestChatCompletionStream(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	response, body := doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model":      SmallModel,
		"stream":     true,
		"max_tokens": 32,
		"messages": []any{
			map[string]any{"role": "user", "content": "this text is ignored for mock-small"},
		},
	}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type = %q", response.Header.Get("Content-Type"))
	}

	content, finishReason := collectCompletionStream(t, body, SmallModel)
	if content != "hello from mock" {
		t.Fatalf("content = %q", content)
	}
	if finishReason != "stop" {
		t.Fatalf("finish reason = %q", finishReason)
	}
}

func TestChatCompletionStreamEcho(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	response, body := doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model":  VisionModel,
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": "streamed echo"},
		},
	}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	content, finishReason := collectCompletionStream(t, body, VisionModel)
	if content != "mock echo: streamed echo" || finishReason != "stop" {
		t.Fatalf("content = %q, finish reason = %q", content, finishReason)
	}
}

func TestChatCompletionStreamChunksLargeUTF8Echo(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	longText := strings.Repeat("界", maxSSETextBytes/3+100)
	response, body := doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model":    VisionModel,
		"stream":   true,
		"messages": []any{map[string]any{"role": "user", "content": longText}},
	}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	content, finishReason := collectCompletionStream(t, body, VisionModel)
	if content != "mock echo: "+longText || finishReason != "stop" || !utf8.ValidString(content) {
		t.Fatalf("large echo was corrupted: finish reason = %q, content length = %d", finishReason, len(content))
	}

	contentChunks := 0
	for _, event := range sseData(t, body) {
		if event == "[DONE]" {
			continue
		}
		var chunk wireCompletionChunk
		if err := json.Unmarshal([]byte(event), &chunk); err != nil {
			t.Fatalf("invalid SSE payload: %v", err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				contentChunks++
			}
		}
	}
	if contentChunks < 2 {
		t.Fatalf("content chunks = %d, want multiple chunks", contentChunks)
	}
}

func TestChatCompletionNonStreaming(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name: "fixed small model text",
			payload: map[string]any{
				"model":      SmallModel,
				"max_tokens": 32,
				"messages":   []any{map[string]any{"role": "user", "content": "ignored"}},
			},
			want: "hello from mock",
		},
		{
			name: "echo last user text from content parts",
			payload: map[string]any{
				"model": VisionModel,
				"messages": []any{
					map[string]any{"role": "user", "content": "older"},
					map[string]any{"role": "assistant", "content": "reply"},
					map[string]any{"role": "user", "content": []any{
						map[string]any{"type": "text", "text": "last "},
						map[string]any{"type": "text", "text": "message"},
						map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}},
					}},
				},
			},
			want: "mock echo: last message",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, body := doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, test.payload), "")
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
			}
			var completion struct {
				ID      string `json:"id"`
				Object  string `json:"object"`
				Model   string `json:"model"`
				Choices []struct {
					Message struct {
						Role    string  `json:"role"`
						Content *string `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage completionUsage `json:"usage"`
			}
			if err := json.Unmarshal(body, &completion); err != nil {
				t.Fatal(err)
			}
			if completion.ID == "" || completion.Object != "chat.completion" || len(completion.Choices) != 1 {
				t.Fatalf("unexpected completion: %+v", completion)
			}
			if completion.Model != test.payload["model"] || completion.Choices[0].Message.Role != "assistant" ||
				completion.Choices[0].Message.Content == nil || *completion.Choices[0].Message.Content != test.want ||
				completion.Choices[0].FinishReason != "stop" {
				t.Fatalf("unexpected message: %+v", completion)
			}
			if completion.Usage.PromptTokens <= 0 || completion.Usage.CompletionTokens <= 0 ||
				completion.Usage.TotalTokens != completion.Usage.PromptTokens+completion.Usage.CompletionTokens {
				t.Fatalf("unexpected usage: %+v", completion.Usage)
			}
		})
	}
}

func TestChatCompletionNonStreamingToolCall(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	response, body := doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model": SmallModel,
		"messages": []any{
			map[string]any{"role": "user", "content": "find the thing"},
		},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":       "lookup",
				"parameters": map[string]any{"type": "object"},
			},
		}},
	}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) != 1 || completion.Choices[0].FinishReason != "tool_calls" ||
		completion.Choices[0].Message.Content != nil || len(completion.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("unexpected tool response: %+v", completion)
	}
	call := completion.Choices[0].Message.ToolCalls[0]
	var arguments map[string]string
	if call.ID == "" || call.Type != "function" || call.Function.Name != "lookup" ||
		json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil || arguments["input"] != "find the thing" {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}

func TestChatCompletionToolStream(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	response, body := doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model":  SmallModel,
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": "find the thing"},
		},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":       "lookup",
				"parameters": map[string]any{"type": "object"},
			},
		}},
	}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	content, finishReason := collectCompletionStream(t, body, SmallModel)
	if content != "" || finishReason != "tool_calls" {
		t.Fatalf("content = %q, finish reason = %q", content, finishReason)
	}

	toolCalls := collectToolCalls(t, body)
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls = %+v", toolCalls)
	}
	call := toolCalls[0]
	var arguments map[string]string
	if call.Index != 0 || call.ID == "" || call.Type != "function" || call.Function.Name != "lookup" ||
		json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil || arguments["input"] != "find the thing" {
		t.Fatalf("unexpected tool call: %+v", call)
	}
}

func TestToolStreamCapsLargeInput(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	longText := strings.Repeat("界", maxToolInputBytes/3+100)
	response, body := doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model":  SmallModel,
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": longText},
		},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":       "lookup",
				"parameters": map[string]any{"type": "object"},
			},
		}},
	}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, http.StatusOK, body)
	}
	toolCalls := collectToolCalls(t, body)
	if len(toolCalls) != 1 {
		t.Fatalf("tool calls = %+v", toolCalls)
	}
	var arguments map[string]string
	if err := json.Unmarshal([]byte(toolCalls[0].Function.Arguments), &arguments); err != nil {
		t.Fatal(err)
	}
	input := arguments["input"]
	if len(input) > maxToolInputBytes || !utf8.ValidString(input) || !strings.HasPrefix(longText, input) {
		t.Fatalf("capped tool input length = %d, valid UTF-8 = %t", len(input), utf8.ValidString(input))
	}
}

func TestAuthToken(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)
	server.SetAuthToken("secret-token")

	for _, authorization := range []string{"", "Bearer wrong-token", "Basic secret-token"} {
		response, body := doRequest(t, server, http.MethodGet, "/models", "", authorization)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d: %s", authorization, response.StatusCode, body)
		}
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Error.Code != "invalid_api_key" {
			t.Fatalf("unexpected auth error: %s (%v)", body, err)
		}
	}

	response, body := doRequest(t, server, http.MethodGet, "/models", "", "Bearer secret-token")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("valid token status = %d: %s", response.StatusCode, body)
	}

	server.SetAuthToken("")
	response, body = doRequest(t, server, http.MethodGet, "/models", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disabled auth status = %d: %s", response.StatusCode, body)
	}
}

func TestFailureStatuses(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	payload := marshalJSON(t, map[string]any{
		"model":    SmallModel,
		"stream":   true,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	server.SetNextStatus(http.StatusTooManyRequests)
	response, body := doRequest(t, server, http.MethodGet, "/models", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health check consumed one-shot status: %d %s", response.StatusCode, body)
	}
	response, body = doRequest(t, server, http.MethodPost, "/chat/completions", payload, "")
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("one-shot status was not reserved for chat: %d %s", response.StatusCode, body)
	}

	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		server.SetNextStatus(status)
		response, body := doRequest(t, server, http.MethodPost, "/chat/completions", payload, "")
		if response.StatusCode != status {
			t.Fatalf("status = %d, want %d: %s", response.StatusCode, status, body)
		}
		if status == http.StatusTooManyRequests && response.Header.Get("Retry-After") == "" {
			t.Fatal("429 response is missing Retry-After")
		}
		var payload struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Error.Message == "" || payload.Error.Code == "" {
			t.Fatalf("invalid error response for %d: %s (%v)", status, body, err)
		}
	}

	response, body = doRequest(t, server, http.MethodPost, "/chat/completions", payload, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("one-shot statuses were not consumed: %d %s", response.StatusCode, body)
	}

	server.SetStatus(SmallModel, http.StatusTooManyRequests)
	for i := 0; i < 2; i++ {
		response, body = doRequest(t, server, http.MethodPost, "/chat/completions", payload, "")
		if response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("persistent status %d = %d: %s", i, response.StatusCode, body)
		}
	}
	visionPayload := marshalJSON(t, map[string]any{
		"model":    VisionModel,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	response, body = doRequest(t, server, http.MethodPost, "/chat/completions", visionPayload, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("model override leaked to another model: %d %s", response.StatusCode, body)
	}

	server.ResetFailures()
	response, body = doRequest(t, server, http.MethodPost, "/chat/completions", payload, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ResetFailures did not clear statuses: %d %s", response.StatusCode, body)
	}
}

func TestRequestCountResetAndDelay(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)
	if count := server.Requests(); count != 0 {
		t.Fatalf("initial requests = %d", count)
	}

	doRequest(t, server, http.MethodGet, "/models", "", "")
	doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model":    SmallModel,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}), "")
	server.SetNextStatus(http.StatusInternalServerError)
	doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model":    SmallModel,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}), "")
	if count := server.Requests(); count != 3 {
		t.Fatalf("requests = %d, want 3", count)
	}

	server.SetDelay(20 * time.Millisecond)
	started := time.Now()
	doRequest(t, server, http.MethodGet, "/models", "", "")
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond {
		t.Fatalf("delay elapsed = %s", elapsed)
	}

	server.SetAuthToken("secret-token")
	server.SetStatus(SmallModel, http.StatusTooManyRequests)
	server.SetDelay(2 * time.Second)
	server.Reset()
	if count := server.Requests(); count != 0 {
		t.Fatalf("requests after reset = %d", count)
	}
	started = time.Now()
	response, body := doRequest(t, server, http.MethodGet, "/models", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Reset preserved auth or delay incorrectly: %d %s", response.StatusCode, body)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("Reset did not clear delay: %s", elapsed)
	}
	response, body = doRequest(t, server, http.MethodPost, "/chat/completions", marshalJSON(t, map[string]any{
		"model":    SmallModel,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}), "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Reset did not clear model status: %d %s", response.StatusCode, body)
	}
}

func TestConcurrentRequestsAreThreadSafe(t *testing.T) {
	server := New()
	t.Cleanup(server.Close)

	const requests = 24
	payload := marshalJSON(t, map[string]any{
		"model":    SmallModel,
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	var wait sync.WaitGroup
	errors := make(chan error, requests)
	for i := 0; i < requests; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := server.Client().Post(server.URL()+"/chat/completions", "application/json", strings.NewReader(payload))
			if err != nil {
				errors <- err
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				errors <- fmt.Errorf("status = %d", response.StatusCode)
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if count := server.Requests(); count != requests {
		t.Fatalf("requests = %d, want %d", count, requests)
	}
}

func doRequest(t *testing.T, server *Server, method, path, body, authorization string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, server.URL()+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}

func marshalJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sseData(t *testing.T, body []byte) []string {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	var events []string
	var data []string
	flush := func() {
		if len(data) == 0 {
			return
		}
		events = append(events, strings.Join(data, "\n"))
		data = nil
	}
	for _, line := range lines {
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return events
}

type wireCompletionChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   *string        `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type wireToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func collectCompletionStream(t *testing.T, body []byte, wantModel string) (string, string) {
	t.Helper()
	events := sseData(t, body)
	if len(events) < 2 || events[len(events)-1] != "[DONE]" {
		t.Fatalf("missing terminal [DONE]: %q", events)
	}
	var content strings.Builder
	var finishReason string
	usageEvents := 0
	for _, event := range events[:len(events)-1] {
		var chunk wireCompletionChunk
		if err := json.Unmarshal([]byte(event), &chunk); err != nil {
			t.Fatalf("invalid SSE payload %q: %v", event, err)
		}
		if chunk.Object != "chat.completion.chunk" || chunk.ID == "" || chunk.Model != wantModel {
			t.Fatalf("unexpected chunk metadata: %+v", chunk)
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				t.Fatalf("choice index = %d", choice.Index)
			}
			if choice.Delta.Content != nil {
				content.WriteString(*choice.Delta.Content)
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			usageEvents++
			if len(chunk.Choices) != 0 || chunk.Usage.PromptTokens <= 0 || chunk.Usage.CompletionTokens <= 0 ||
				chunk.Usage.TotalTokens != chunk.Usage.PromptTokens+chunk.Usage.CompletionTokens {
				t.Fatalf("invalid usage event: %+v", chunk)
			}
		}
	}
	if usageEvents != 1 {
		t.Fatalf("usage events = %d", usageEvents)
	}
	var last wireCompletionChunk
	if err := json.Unmarshal([]byte(events[len(events)-2]), &last); err != nil || last.Usage == nil {
		t.Fatalf("last event before [DONE] is not usage: %s (%v)", events[len(events)-2], err)
	}
	return content.String(), finishReason
}

func collectToolCalls(t *testing.T, body []byte) []wireToolCall {
	t.Helper()
	var toolCalls []wireToolCall
	for _, event := range sseData(t, body) {
		if event == "[DONE]" {
			continue
		}
		var chunk wireCompletionChunk
		if err := json.Unmarshal([]byte(event), &chunk); err != nil {
			t.Fatalf("invalid SSE payload %q: %v", event, err)
		}
		for _, choice := range chunk.Choices {
			toolCalls = append(toolCalls, choice.Delta.ToolCalls...)
		}
	}
	return toolCalls
}
