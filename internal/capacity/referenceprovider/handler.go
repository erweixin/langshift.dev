// Package referenceprovider implements the controlled, externally routable
// provider used only by the Stage 3 reference-production capacity gate.
package referenceprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrInvalid = errors.New("reference provider configuration is invalid")

const (
	ScenarioRuntimeArtifact = "provider_runtime_artifact"
	ScenarioLocalHold       = "provider_local_hold"
	ScenarioReplay          = "provider_replay"
)

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Metrics interface {
	Observe(context.Context, Observation)
}

type Observation struct {
	Scenario, Outcome string
	Duration          time.Duration
	InputTokens       uint64
	OutputTokens      uint64
}

type Config struct {
	Model, BearerToken        string
	LocalHold, ReplayHold     time.Duration
	RuntimeArtifactBytes      int
	RuntimeHoldMilliseconds   int
	MaximumRequestBytes       int64
	MaximumConcurrentRequests int64
	ReplayRateNumerator       uint8
	Metrics                   Metrics
	Now                       func() time.Time
}

type Handler struct {
	configuration Config
	tokenHash     [32]byte
	sequence      atomic.Uint64
	inflight      atomic.Int64
	replays       replaySet
}

type replaySet struct {
	mu      sync.Mutex
	entries map[[32]byte]time.Time
}

type wireRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             []json.RawMessage `json:"input"`
	Tools             []wireTool        `json:"tools,omitempty"`
	ToolChoice        json.RawMessage   `json:"tool_choice,omitempty"`
	MaxOutputTokens   uint32            `json:"max_output_tokens"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Stream            bool              `json:"stream"`
	Store             bool              `json:"store"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
}

type wireTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

func New(config Config) (*Handler, error) {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.MaximumRequestBytes == 0 {
		config.MaximumRequestBytes = 8 << 20
	}
	if config.MaximumConcurrentRequests == 0 {
		config.MaximumConcurrentRequests = 4096
	}
	if config.ReplayRateNumerator == 0 {
		config.ReplayRateNumerator = 153
	}
	if !modelPattern.MatchString(config.Model) || len(config.BearerToken) < 32 || len(config.BearerToken) > 16<<10 || strings.ContainsAny(config.BearerToken, "\x00\r\n") || config.LocalHold < 100*time.Millisecond || config.LocalHold > 5*time.Second || config.ReplayHold < 0 || config.ReplayHold > 5*time.Second || config.RuntimeArtifactBytes < 1<<20 || config.RuntimeArtifactBytes > 8<<20 || config.RuntimeHoldMilliseconds < 100 || config.RuntimeHoldMilliseconds > 10_000 || config.MaximumRequestBytes < 1024 || config.MaximumRequestBytes > 64<<20 || config.MaximumConcurrentRequests < 200 || config.MaximumConcurrentRequests > 100_000 {
		return nil, ErrInvalid
	}
	return &Handler{configuration: config, tokenHash: sha256.Sum256([]byte(config.BearerToken)), replays: replaySet{entries: map[[32]byte]time.Time{}}}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := handler.configuration.Now().UTC()
	if request.URL.Path != "/v1/responses" || request.Method != http.MethodPost || !handler.authorized(request.Header.Get("Authorization")) || !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writer.Header().Set("Cache-Control", "no-store")
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	current := handler.inflight.Add(1)
	defer handler.inflight.Add(-1)
	if current > handler.configuration.MaximumConcurrentRequests {
		handler.failure(request.Context(), writer, "overloaded", http.StatusServiceUnavailable, started, "unknown")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, handler.configuration.MaximumRequestBytes+1))
	if err != nil || len(body) == 0 || int64(len(body)) > handler.configuration.MaximumRequestBytes {
		handler.failure(request.Context(), writer, "invalid_request", http.StatusBadRequest, started, "unknown")
		return
	}
	var wire wireRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(&struct{}{}) != io.EOF || !handler.valid(wire) {
		handler.failure(request.Context(), writer, "invalid_request", http.StatusBadRequest, started, "unknown")
		return
	}
	scenario, hasToolResult := scenarioOf(wire.Input)
	digest := sha256.Sum256(body)
	if scenario == ScenarioReplay && !hasToolResult && digest[0] < handler.configuration.ReplayRateNumerator && handler.replays.first(digest, handler.configuration.Now().UTC()) {
		if !wait(request.Context(), handler.configuration.ReplayHold) {
			return
		}
		writer.Header().Set("Retry-After", "1")
		handler.failure(request.Context(), writer, "reference_replay", http.StatusServiceUnavailable, started, scenario)
		return
	}
	if scenario == ScenarioLocalHold && !wait(request.Context(), handler.configuration.LocalHold) {
		return
	}
	identifier := fmt.Sprintf("resp_ref_%s_%d", hex.EncodeToString(digest[:6]), handler.sequence.Add(1))
	response := handler.response(identifier, wire, scenario, hasToolResult, digest)
	if wire.Stream {
		handler.stream(writer, response)
	} else {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(writer).Encode(response)
	}
	handler.observe(request.Context(), Observation{Scenario: scenario, Outcome: "completed", Duration: handler.configuration.Now().UTC().Sub(started), InputTokens: 100, OutputTokens: 200})
}

func (handler *Handler) valid(request wireRequest) bool {
	if request.Model != handler.configuration.Model || len(request.Input) == 0 || len(request.Input) > 4096 || request.MaxOutputTokens == 0 || request.Store || !request.ParallelToolCalls || len(request.Tools) > 128 {
		return false
	}
	for _, tool := range request.Tools {
		if tool.Type != "function" || tool.Name == "" || !tool.Strict || !validObject(tool.Parameters) {
			return false
		}
	}
	return true
}

func (handler *Handler) response(id string, request wireRequest, scenario string, hasToolResult bool, digest [32]byte) map[string]any {
	output := []any{}
	if scenario == ScenarioRuntimeArtifact && !hasToolResult && hasTool(request.Tools, "reference_artifact") {
		arguments, _ := json.Marshal(map[string]int{"bytes": handler.configuration.RuntimeArtifactBytes, "hold_milliseconds": handler.configuration.RuntimeHoldMilliseconds})
		output = append(output, map[string]any{"type": "function_call", "call_id": "call_" + hex.EncodeToString(digest[:10]), "name": "reference_artifact", "arguments": string(arguments)})
	} else {
		text := referenceText(scenario)
		output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}})
	}
	return map[string]any{"id": id, "object": "response", "created_at": handler.configuration.Now().UTC().Unix(), "model": request.Model, "status": "completed", "output": output, "usage": map[string]uint64{"input_tokens": 100, "output_tokens": 200}}
}

func (handler *Handler) stream(writer http.ResponseWriter, response map[string]any) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	if text := responseText(response); text != "" {
		delta, _ := json.Marshal(map[string]string{"type": "response.output_text.delta", "delta": text})
		_, _ = fmt.Fprintf(writer, "event: response.output_text.delta\ndata: %s\n\n", delta)
	}
	terminal, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
	_, _ = fmt.Fprintf(writer, "event: response.completed\ndata: %s\n\n", terminal)
}

func (handler *Handler) failure(ctx context.Context, writer http.ResponseWriter, code string, status int, started time.Time, scenario string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": code, "message": "controlled reference provider failure"}})
	handler.observe(ctx, Observation{Scenario: scenario, Outcome: code, Duration: handler.configuration.Now().UTC().Sub(started)})
}

func (handler *Handler) authorized(value string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	digest := sha256.Sum256([]byte(strings.TrimPrefix(value, prefix)))
	return subtle.ConstantTimeCompare(digest[:], handler.tokenHash[:]) == 1
}

func (handler *Handler) observe(ctx context.Context, observation Observation) {
	if handler.configuration.Metrics != nil {
		handler.configuration.Metrics.Observe(ctx, observation)
	}
}

func (set *replaySet) first(digest [32]byte, now time.Time) bool {
	set.mu.Lock()
	defer set.mu.Unlock()
	if expires, exists := set.entries[digest]; exists && expires.After(now) {
		return false
	}
	if len(set.entries) >= 100_000 {
		for key, expires := range set.entries {
			if !expires.After(now) {
				delete(set.entries, key)
			}
		}
		if len(set.entries) >= 100_000 {
			return false
		}
	}
	set.entries[digest] = now.Add(10 * time.Minute)
	return true
}

func scenarioOf(inputs []json.RawMessage) (string, bool) {
	joined := make([]byte, 0, 1024)
	hasToolResult := false
	for _, input := range inputs {
		var value map[string]json.RawMessage
		if json.Unmarshal(input, &value) != nil {
			continue
		}
		var kind string
		_ = json.Unmarshal(value["type"], &kind)
		if kind == "function_call_output" {
			hasToolResult = true
		}
		joined = append(joined, input...)
	}
	text := string(joined)
	for _, scenario := range []string{ScenarioRuntimeArtifact, ScenarioLocalHold, ScenarioReplay} {
		if strings.Contains(text, "[lites-reference-capacity:"+scenario+"]") {
			return scenario, hasToolResult
		}
	}
	return "default", hasToolResult
}

func hasTool(tools []wireTool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func validObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return len(value) > 0 && json.Unmarshal(value, &object) == nil && object != nil
}

func responseText(response map[string]any) string {
	outputs, _ := response["output"].([]any)
	if len(outputs) != 1 {
		return ""
	}
	message, _ := outputs[0].(map[string]any)
	contents, _ := message["content"].([]any)
	if len(contents) != 1 {
		return ""
	}
	content, _ := contents[0].(map[string]any)
	text, _ := content["text"].(string)
	return text
}

func referenceText(scenario string) string {
	return "Reference scenario " + scenario + " completed under the immutable Stage 3 capacity profile. " + strings.Repeat("verified ", 80)
}

func wait(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
