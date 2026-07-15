// Package provider translates the gateway's provider-neutral request into
// provider wire protocols. It deliberately depends on a narrow HTTP boundary
// so credentials and endpoint policy remain owned by the egress broker.
package provider

import (
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
	Type       string
	Text       string
	MediaType  string
	DataBase64 string
	ToolUseID  string
	ToolName   string
	ToolInput  json.RawMessage
	IsError    bool
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
