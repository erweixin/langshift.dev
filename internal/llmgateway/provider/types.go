// Package provider translates the gateway's provider-neutral request into
// provider wire protocols. It deliberately depends on a narrow HTTP boundary
// so credentials and endpoint policy remain owned by the egress broker.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

var (
	ErrInvalidRequest   = errors.New("provider request is invalid")
	ErrRequestTooLarge  = errors.New("provider request exceeded its size limit")
	ErrResponseInvalid  = errors.New("provider response is invalid")
	ErrStreamIncomplete = errors.New("provider stream ended before a terminal event")
)

const (
	ClassRateLimited      = "rate_limited"
	ClassContextTooLong   = "context_too_long"
	ClassContentFiltered  = "content_filtered"
	ClassModelUnavailable = "model_unavailable"
	ClassAuthFailed       = "auth_failed"
	ClassProviderError    = "provider_error"
	ClassTimeout          = "timeout"
	ClassInvalidRequest   = "invalid_request"
	ClassCancelled        = "cancelled"
)

type Client interface {
	Do(context.Context, string, string, http.Header, io.Reader) (*http.Response, error)
}

type Adapter interface {
	Prepare(Request) (PreparedRequest, error)
	ExecutePrepared(context.Context, Client, PreparedRequest, DeltaSink) (Result, error)
}

// PreparedRequest is the immutable wire representation hashed before durable
// dispatch authorization. Its payload is intentionally private so callers
// cannot change the provider request after authorizing its hash.
type PreparedRequest struct {
	adapter, requestHash string
	payload              []byte
	stream               bool
}

func (request PreparedRequest) RequestHash() string { return request.requestHash }

func prepared(adapter string, payload []byte, requestHash string, stream bool) PreparedRequest {
	return PreparedRequest{adapter: adapter, payload: append([]byte(nil), payload...), requestHash: requestHash, stream: stream}
}

func (request PreparedRequest) valid(adapter string) bool {
	return request.adapter == adapter && len(request.payload) > 0 && request.requestHash != "" && hashBytes(request.payload) == request.requestHash
}

type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []Tool
	ToolChoice      ToolChoice
	MaxOutputTokens uint32
	Temperature     *float64
	TopP            *float64
	Stream          bool
}

type Message struct {
	Role    string
	Content []ContentBlock
}

type ContentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	MediaType  string          `json:"media_type,omitempty"`
	DataBase64 string          `json:"data_base64,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

// UnmarshalJSON keeps run-message envelopes written before the canonical
// snake_case encoding readable. New writes use the field tags above; reads
// accept exactly one of each canonical or legacy Go-field spelling and reject
// unknown or ambiguous fields instead of weakening the closed message schema.
func (block *ContentBlock) UnmarshalJSON(encoded []byte) error {
	if block == nil {
		return errors.New("content block destination is nil")
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return errors.New("content block is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("content block is invalid")
	}
	allowed := map[string]bool{
		"type": true, "text": true, "media_type": true, "data_base64": true,
		"tool_use_id": true, "tool_name": true, "tool_input": true, "is_error": true,
		"Type": true, "Text": true, "MediaType": true, "DataBase64": true,
		"ToolUseID": true, "ToolName": true, "ToolInput": true, "IsError": true,
	}
	for name := range fields {
		if !allowed[name] {
			return errors.New("content block contains an unknown field")
		}
	}
	read := func(canonical, legacy string, target any) error {
		canonicalValue, hasCanonical := fields[canonical]
		legacyValue, hasLegacy := fields[legacy]
		if hasCanonical && hasLegacy {
			return errors.New("content block contains duplicate field spellings")
		}
		if !hasCanonical && !hasLegacy {
			return nil
		}
		if hasLegacy {
			canonicalValue = legacyValue
		}
		return json.Unmarshal(canonicalValue, target)
	}
	var decoded ContentBlock
	if err := read("type", "Type", &decoded.Type); err != nil {
		return err
	}
	if err := read("text", "Text", &decoded.Text); err != nil {
		return err
	}
	if err := read("media_type", "MediaType", &decoded.MediaType); err != nil {
		return err
	}
	if err := read("data_base64", "DataBase64", &decoded.DataBase64); err != nil {
		return err
	}
	if err := read("tool_use_id", "ToolUseID", &decoded.ToolUseID); err != nil {
		return err
	}
	if err := read("tool_name", "ToolName", &decoded.ToolName); err != nil {
		return err
	}
	if err := read("tool_input", "ToolInput", &decoded.ToolInput); err != nil {
		return err
	}
	if err := read("is_error", "IsError", &decoded.IsError); err != nil {
		return err
	}
	*block = decoded
	return nil
}

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Strict      bool
}

type ToolChoice struct {
	Mode string
	Name string
}

type Delta struct {
	Kind      string
	Text      string
	ToolUseID string
	ToolName  string
	JSON      string
	At        time.Time
}

type DeltaSink interface {
	OnDelta(context.Context, Delta) error
}

type DeltaSinkFunc func(context.Context, Delta) error

func (function DeltaSinkFunc) OnDelta(ctx context.Context, delta Delta) error {
	return function(ctx, delta)
}

type Result struct {
	ProviderRequestID      string
	Model                  string
	Status                 string
	FinishReason           string
	Text                   string
	Refusal                string
	ToolCalls              []ToolCall
	InputTokens            uint64
	OutputTokens           uint64
	RequestHash            string
	ResponseHash           string
	VisibleOutputStartedAt *time.Time
}

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type CallError struct {
	Class             string
	Code              string
	HTTPStatus        int
	ProviderRequestID string
	RetryAfter        time.Duration
	OutcomeUnknown    bool
	Cause             error
}

func (callError *CallError) Error() string {
	if callError == nil {
		return "provider call failed"
	}
	return "provider call failed: " + callError.Class
}

func (callError *CallError) Unwrap() error {
	if callError == nil {
		return nil
	}
	return callError.Cause
}
