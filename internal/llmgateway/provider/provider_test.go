package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type clientFunc func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error)

func (function clientFunc) Do(ctx context.Context, method, path string, headers http.Header, body io.Reader) (*http.Response, error) {
	return function(ctx, method, path, headers, body)
}

func baseRequest(stream bool) Request {
	temperature, topP := 0.4, 0.9
	return Request{
		Model: "production-model-2026-07-01", System: "Be exact.", Stream: stream,
		MaxOutputTokens: 2048, Temperature: &temperature, TopP: &topP,
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Weather?"}, {Type: "image", MediaType: "image/png", DataBase64: "aW1hZ2U="}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "tool_use", ToolUseID: "call_1", ToolName: "weather", ToolInput: json.RawMessage(`{"city":"Paris"}`)}}},
			{Role: "user", Content: []ContentBlock{{Type: "tool_result", ToolUseID: "call_1", Text: "18 C"}}},
		},
		Tools:      []Tool{{Name: "weather", Description: "Get weather", InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`), Strict: true}},
		ToolChoice: ToolChoice{Mode: "auto"},
	}
}

func response(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestOpenAIResponsesRequestAndNormalizedResult(t *testing.T) {
	adapter, err := NewOpenAI(OpenAIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFunc(func(_ context.Context, method, path string, headers http.Header, body io.Reader) (*http.Response, error) {
		if method != http.MethodPost || path != "responses" || headers.Get("Authorization") != "" || headers.Get("Content-Type") != "application/json" {
			t.Fatalf("unsafe request boundary: method=%s path=%s headers=%v", method, path, headers)
		}
		payload, readErr := io.ReadAll(body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var wire map[string]any
		if json.Unmarshal(payload, &wire) != nil || wire["store"] != false || wire["stream"] != false || wire["parallel_tool_calls"] != true || wire["max_output_tokens"] != float64(2048) {
			t.Fatalf("wire request = %s", payload)
		}
		encoded := string(payload)
		for _, expected := range []string{`"type":"input_image"`, `"type":"function_call"`, `"type":"function_call_output"`, `"strict":true`} {
			if !strings.Contains(encoded, expected) {
				t.Fatalf("wire request missing %s: %s", expected, payload)
			}
		}
		return response(200, "application/json; charset=utf-8", `{"id":"resp_1","model":"production-model-2026-07-01","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Use an umbrella."}]},{"type":"function_call","call_id":"call_2","name":"weather","arguments":"{\"city\":\"Rome\"}"}],"usage":{"input_tokens":91,"output_tokens":12}}`), nil
	})
	prepared, err := adapter.Prepare(baseRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.ExecutePrepared(t.Context(), client, prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderRequestID != "resp_1" || result.Text != "Use an umbrella." || result.FinishReason != "tool_use" || result.InputTokens != 91 || result.OutputTokens != 12 || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call_2" || len(result.RequestHash) != 64 || len(result.ResponseHash) != 64 {
		t.Fatalf("normalized result = %#v", result)
	}
}

func TestOpenAIResponsesStreamRequiresMatchingTerminalResponse(t *testing.T) {
	instant := time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC)
	adapter, _ := NewOpenAI(OpenAIConfig{Now: func() time.Time { return instant }})
	stream := "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"model\":\"production-model-2026-07-01\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello world\"}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}}\n\n"
	var deltas []string
	client := clientFunc(func(_ context.Context, _, _ string, headers http.Header, _ io.Reader) (*http.Response, error) {
		if headers.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept = %q", headers.Get("Accept"))
		}
		return response(200, "text/event-stream", stream), nil
	})
	prepared, err := adapter.Prepare(baseRequest(true))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.ExecutePrepared(t.Context(), client, prepared, DeltaSinkFunc(func(_ context.Context, delta Delta) error {
		deltas = append(deltas, delta.Text)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Hello world" || result.VisibleOutputStartedAt == nil || !result.VisibleOutputStartedAt.Equal(instant) || strings.Join(deltas, "") != result.Text || result.InputTokens != 4 || result.OutputTokens != 2 {
		t.Fatalf("stream result=%#v deltas=%v", result, deltas)
	}

	missingTerminal := clientFunc(func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error) {
		return response(200, "text/event-stream", "event: response.output_text.delta\ndata: {\"delta\":\"partial\"}\n\n"), nil
	})
	_, err = adapter.ExecutePrepared(t.Context(), missingTerminal, prepared, nil)
	var callErr *CallError
	if !errors.As(err, &callErr) || !callErr.OutcomeUnknown || !errors.Is(err, ErrStreamIncomplete) {
		t.Fatalf("unterminated stream = %#v", err)
	}

	afterTerminal := clientFunc(func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error) {
		return response(200, "text/event-stream", stream+"event: response.output_text.delta\ndata: {\"delta\":\"illegal\"}\n\n"), nil
	})
	if _, err = adapter.ExecutePrepared(t.Context(), afterTerminal, prepared, nil); !errors.Is(err, ErrResponseInvalid) {
		t.Fatalf("post-terminal event accepted: %v", err)
	}
}

func TestAnthropicMessagesRequestAndNormalizedResult(t *testing.T) {
	adapter, err := NewAnthropic(AnthropicConfig{})
	if err != nil {
		t.Fatal(err)
	}
	client := clientFunc(func(_ context.Context, method, path string, headers http.Header, body io.Reader) (*http.Response, error) {
		payload, _ := io.ReadAll(body)
		if method != http.MethodPost || path != "messages" || headers.Get("Anthropic-Version") != "2023-06-01" || headers.Get("X-Api-Key") != "" || !bytes.Contains(payload, []byte(`"input_schema"`)) || !bytes.Contains(payload, []byte(`"tool_result"`)) {
			t.Fatalf("Anthropic request path=%s headers=%v body=%s", path, headers, payload)
		}
		return response(200, "application/json", `{"id":"msg_1","model":"production-model-2026-07-01","stop_reason":"tool_use","content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_2","name":"weather","input":{"city":"Berlin"}}],"usage":{"input_tokens":70,"output_tokens":19}}`), nil
	})
	prepared, err := adapter.Prepare(baseRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.ExecutePrepared(t.Context(), client, prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderRequestID != "msg_1" || result.Text != "Checking." || result.FinishReason != "tool_use" || len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "weather" || result.InputTokens != 70 || result.OutputTokens != 19 {
		t.Fatalf("normalized result = %#v", result)
	}
}

func TestAnthropicMessagesStreamAccumulatesTextToolsAndUsage(t *testing.T) {
	instant := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	adapter, _ := NewAnthropic(AnthropicConfig{Now: func() time.Time { return instant }})
	stream := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"model\":\"production-model-2026-07-01\",\"usage\":{\"input_tokens\":33}}}\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Calling\"}}\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_3\",\"name\":\"weather\",\"input\":{}}}\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Oslo\\\"}\"}}\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":17}}\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
	}, "\n")
	var kinds []string
	client := clientFunc(func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error) {
		return response(200, "text/event-stream; charset=utf-8", stream), nil
	})
	prepared, err := adapter.Prepare(baseRequest(true))
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.ExecutePrepared(t.Context(), client, prepared, DeltaSinkFunc(func(_ context.Context, delta Delta) error {
		kinds = append(kinds, delta.Kind)
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderRequestID != "msg_stream" || result.Text != "Calling" || result.FinishReason != "tool_use" || result.InputTokens != 33 || result.OutputTokens != 17 || len(result.ToolCalls) != 1 || string(result.ToolCalls[0].Input) != `{"city":"Oslo"}` || result.VisibleOutputStartedAt == nil || !result.VisibleOutputStartedAt.Equal(instant) || strings.Join(kinds, ",") != "text,tool_input" {
		t.Fatalf("stream result=%#v kinds=%v", result, kinds)
	}
}

func TestProviderHTTPAndTransportErrorsPreserveRetrySemantics(t *testing.T) {
	adapter, _ := NewOpenAI(OpenAIConfig{Now: func() time.Time { return time.Unix(0, 0) }})
	rateLimited := clientFunc(func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error) {
		result := response(429, "application/json", `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`)
		result.Header.Set("Retry-After", "7")
		result.Header.Set("x-request-id", "request_429")
		return result, nil
	})
	prepared, err := adapter.Prepare(baseRequest(false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ExecutePrepared(t.Context(), rateLimited, prepared, nil)
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.Class != ClassRateLimited || callErr.RetryAfter != 7*time.Second || callErr.ProviderRequestID != "request_429" || callErr.OutcomeUnknown {
		t.Fatalf("rate limit error = %#v", err)
	}

	network := clientFunc(func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})
	_, err = adapter.ExecutePrepared(t.Context(), network, prepared, nil)
	if !errors.As(err, &callErr) || callErr.Class != ClassTimeout || !callErr.OutcomeUnknown {
		t.Fatalf("network error = %#v", err)
	}
}

func TestProviderRequestValidationFailsBeforeNetwork(t *testing.T) {
	adapter, _ := NewOpenAI(OpenAIConfig{MaximumRequestBytes: 1024})
	request := baseRequest(false)
	request.Messages[0].Content[1].DataBase64 = "not-base64"
	if _, err := adapter.Prepare(request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid image: err=%v", err)
	}
	request = baseRequest(false)
	request.Messages[0].Content[0].Text = strings.Repeat("x", 2048)
	if _, err := adapter.Prepare(request); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("oversized request: err=%v", err)
	}
}
