package referenceprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langshift/lites/internal/llmgateway/provider"
)

type adapterClient struct {
	baseURL, token string
	client         *http.Client
	captured       *string
}

func (client adapterClient) Do(ctx context.Context, method, path string, headers http.Header, body io.Reader) (*http.Response, error) {
	if client.captured != nil {
		encoded, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		*client.captured = string(encoded)
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+"/v1/"+path, body)
	if err != nil {
		return nil, err
	}
	request.Header = headers.Clone()
	request.Header.Set("Authorization", "Bearer "+client.token)
	return client.client.Do(request)
}

type observations struct {
	mu     sync.Mutex
	values []Observation
}

func (metrics *observations) Observe(_ context.Context, value Observation) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.values = append(metrics.values, value)
}

func TestHandlerProducesAdapterVerifiedStreamAndToolCall(t *testing.T) {
	metrics := &observations{}
	handler := newTestHandler(t, metrics)
	server := httptest.NewServer(handler)
	defer server.Close()
	adapter, err := provider.NewOpenAI(provider.OpenAIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var captured string
	client := adapterClient{baseURL: server.URL, token: testToken, client: server.Client(), captured: &captured}
	request := provider.Request{
		Model: "reference-model-2026-07-16", System: "reference", MaxOutputTokens: 512, Stream: true,
		Messages:   []provider.Message{{Role: "user", Content: []provider.ContentBlock{{Type: "text", Text: "[lites-reference-capacity:provider_runtime_artifact] Execute."}}}},
		Tools:      []provider.Tool{{Name: "reference_artifact", Description: "reference", InputSchema: json.RawMessage(`{"type":"object","properties":{"bytes":{"type":"integer"},"hold_milliseconds":{"type":"integer"}},"required":["bytes","hold_milliseconds"],"additionalProperties":false}`), Strict: true}},
		ToolChoice: provider.ToolChoice{Mode: "auto"},
	}
	prepared, err := adapter.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.ExecutePrepared(t.Context(), client, prepared, provider.DeltaSinkFunc(func(context.Context, provider.Delta) error { return nil }))
	if err != nil || result.FinishReason != "tool_use" || len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "reference_artifact" || result.InputTokens != 100 || result.OutputTokens != 200 {
		t.Fatalf("result=%#v error=%v observations=%#v request=%s", result, err, metrics.values, captured)
	}
	var input struct {
		Bytes            int `json:"bytes"`
		HoldMilliseconds int `json:"hold_milliseconds"`
	}
	if json.Unmarshal(result.ToolCalls[0].Input, &input) != nil || input.Bytes != 1<<20 || input.HoldMilliseconds != 1000 {
		t.Fatalf("tool input=%s", result.ToolCalls[0].Input)
	}

	request.Messages = append(request.Messages,
		provider.Message{Role: "assistant", Content: []provider.ContentBlock{{Type: "tool_use", ToolUseID: result.ToolCalls[0].ID, ToolName: result.ToolCalls[0].Name, ToolInput: result.ToolCalls[0].Input}}},
		provider.Message{Role: "user", Content: []provider.ContentBlock{{Type: "tool_result", ToolUseID: result.ToolCalls[0].ID, Text: `{"artifact":"complete"}`}}},
	)
	prepared, err = adapter.Prepare(request)
	if err != nil {
		t.Fatal(err)
	}
	deltas := 0
	result, err = adapter.ExecutePrepared(t.Context(), client, prepared, provider.DeltaSinkFunc(func(_ context.Context, delta provider.Delta) error {
		if delta.Kind == "text" {
			deltas++
		}
		return nil
	}))
	if err != nil || result.FinishReason != "stop" || result.Text == "" || deltas != 1 {
		t.Fatalf("terminal result=%#v deltas=%d error=%v", result, deltas, err)
	}
	if len(metrics.values) != 2 || metrics.values[0].Scenario != ScenarioRuntimeArtifact {
		t.Fatalf("observations=%#v", metrics.values)
	}
}

func TestHandlerReplayFailsOnceThenSucceeds(t *testing.T) {
	handler := newTestHandler(t, nil)
	server := httptest.NewServer(handler)
	defer server.Close()
	adapter, _ := provider.NewOpenAI(provider.OpenAIConfig{})
	client := adapterClient{baseURL: server.URL, token: testToken, client: server.Client()}
	var prepared provider.PreparedRequest
	for suffix := 0; suffix < 1000; suffix++ {
		request := provider.Request{Model: "reference-model-2026-07-16", MaxOutputTokens: 512, Stream: false, Messages: []provider.Message{{Role: "user", Content: []provider.ContentBlock{{Type: "text", Text: "[lites-reference-capacity:provider_replay] " + strings.Repeat("x", suffix+1)}}}}, ToolChoice: provider.ToolChoice{Mode: "none"}}
		candidate, err := adapter.Prepare(request)
		if err != nil {
			t.Fatal(err)
		}
		result, executeErr := adapter.ExecutePrepared(t.Context(), client, candidate, nil)
		if executeErr != nil {
			var callError *provider.CallError
			if !errors.As(executeErr, &callError) || callError.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("first error=%v result=%#v", executeErr, result)
			}
			prepared = candidate
			break
		}
	}
	if prepared.RequestHash() == "" {
		t.Fatal("could not select a deterministic replay sample")
	}
	result, err := adapter.ExecutePrepared(t.Context(), client, prepared, nil)
	if err != nil || result.FinishReason != "stop" || result.Text == "" {
		t.Fatalf("replay result=%#v error=%v", result, err)
	}
}

func TestHandlerRejectsUnauthenticatedAndUnknownInput(t *testing.T) {
	handler := newTestHandler(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"unknown":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unauthenticated status=%d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"unknown":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+testToken)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

const testToken = "reference-provider-test-token-00000000000000000000"

func newTestHandler(t *testing.T, metrics Metrics) *Handler {
	t.Helper()
	handler, err := New(Config{Model: "reference-model-2026-07-16", BearerToken: testToken, LocalHold: 100 * time.Millisecond, RuntimeArtifactBytes: 1 << 20, RuntimeHoldMilliseconds: 1000, MaximumConcurrentRequests: 200, ReplayRateNumerator: 255, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
