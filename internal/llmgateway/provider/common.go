package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaximumRequestBytes = 8 << 20
	defaultMaximumEventBytes   = 4 << 20
	defaultMaximumTextBytes    = 16 << 20
	maximumToolCount           = 128
	maximumMessageCount        = 4096
)

type limits struct {
	requestBytes int
	eventBytes   int
	textBytes    int
	now          func() time.Time
}

func newLimits(requestBytes, eventBytes, textBytes int, now func() time.Time) (limits, error) {
	if requestBytes == 0 {
		requestBytes = defaultMaximumRequestBytes
	}
	if eventBytes == 0 {
		eventBytes = defaultMaximumEventBytes
	}
	if textBytes == 0 {
		textBytes = defaultMaximumTextBytes
	}
	if requestBytes < 1024 || requestBytes > 64<<20 || eventBytes < 1024 || eventBytes > 16<<20 || textBytes < 1024 || textBytes > 64<<20 {
		return limits{}, ErrInvalidRequest
	}
	if now == nil {
		now = time.Now
	}
	return limits{requestBytes: requestBytes, eventBytes: eventBytes, textBytes: textBytes, now: now}, nil
}

func marshalRequest(request Request, wire any, maximum int) ([]byte, string, error) {
	if err := validateRequest(request); err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, "", ErrInvalidRequest
	}
	if len(payload) > maximum {
		return nil, "", ErrRequestTooLarge
	}
	return payload, hashBytes(payload), nil
}

func validateRequest(request Request) error {
	if request.Model == "" || len(request.Model) > 256 || request.MaxOutputTokens == 0 || len(request.Messages) == 0 || len(request.Messages) > maximumMessageCount || len(request.Tools) > maximumToolCount {
		return ErrInvalidRequest
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 2) || request.TopP != nil && (*request.TopP <= 0 || *request.TopP > 1) {
		return ErrInvalidRequest
	}
	for _, message := range request.Messages {
		if message.Role != "user" && message.Role != "assistant" || len(message.Content) == 0 {
			return ErrInvalidRequest
		}
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				if block.Text == "" {
					return ErrInvalidRequest
				}
			case "image":
				if !allowedImageType(block.MediaType) || block.DataBase64 == "" || !validBase64(block.DataBase64) {
					return ErrInvalidRequest
				}
			case "tool_use":
				if message.Role != "assistant" || block.ToolUseID == "" || !validName(block.ToolName) || !validJSONObject(block.ToolInput) {
					return ErrInvalidRequest
				}
			case "tool_result":
				if message.Role != "user" || block.ToolUseID == "" || block.Text == "" {
					return ErrInvalidRequest
				}
			default:
				return ErrInvalidRequest
			}
		}
	}
	seen := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		if !validName(tool.Name) || !validJSONObject(tool.InputSchema) {
			return ErrInvalidRequest
		}
		if _, exists := seen[tool.Name]; exists {
			return ErrInvalidRequest
		}
		seen[tool.Name] = struct{}{}
	}
	switch request.ToolChoice.Mode {
	case "", "auto", "required", "none":
		if request.ToolChoice.Name != "" {
			return ErrInvalidRequest
		}
	case "tool":
		if _, exists := seen[request.ToolChoice.Name]; !exists {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func validName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character == '_' || character == '-' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func validJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func allowedImageType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func validBase64(value string) bool {
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(value))
	_, err := io.Copy(io.Discard, decoder)
	return err == nil
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func readResponse(body io.Reader, maximum int) ([]byte, string, error) {
	payload, err := io.ReadAll(io.LimitReader(body, int64(maximum)+1))
	if err != nil {
		return nil, "", err
	}
	if len(payload) > maximum {
		return nil, "", ErrResponseInvalid
	}
	return payload, hashBytes(payload), nil
}

type sseEvent struct {
	Name string
	Data []byte
}

func consumeSSE(reader io.Reader, maximumEventBytes int, consume func(sseEvent) error) (string, error) {
	hasher := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(reader, hasher))
	scanner.Buffer(make([]byte, 64<<10), maximumEventBytes+1024)
	var name string
	var data bytes.Buffer
	dispatch := func() error {
		if data.Len() == 0 {
			name = ""
			return nil
		}
		payload := bytes.TrimSuffix(data.Bytes(), []byte("\n"))
		if len(payload) > maximumEventBytes {
			return ErrResponseInvalid
		}
		err := consume(sseEvent{Name: name, Data: append([]byte(nil), payload...)})
		name = ""
		data.Reset()
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return "", err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch field {
		case "event":
			name = value
		case "data":
			if data.Len()+len(value)+1 > maximumEventBytes {
				return "", ErrResponseInvalid
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if err := dispatch(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func emitDelta(ctx context.Context, sink DeltaSink, limit int, visible **time.Time, now func() time.Time, delta Delta) error {
	if len(delta.Text)+len(delta.JSON) > limit {
		return ErrResponseInvalid
	}
	if delta.At.IsZero() {
		delta.At = now().UTC()
	}
	if *visible == nil && (delta.Text != "" || delta.JSON != "") {
		value := delta.At
		*visible = &value
	}
	if sink == nil {
		return nil
	}
	return sink.OnDelta(ctx, delta)
}

func retryAfter(header http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if target, err := http.ParseTime(value); err == nil && target.After(now) {
		return target.Sub(now)
	}
	return 0
}

type providerErrorEnvelope struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func httpCallError(response *http.Response, payload []byte, now time.Time) error {
	var envelope providerErrorEnvelope
	_ = json.Unmarshal(payload, &envelope)
	code := strings.ToLower(strings.TrimSpace(envelope.Error.Code))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(envelope.Error.Type))
	}
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(envelope.Type))
	}
	requestID := response.Header.Get("x-request-id")
	if requestID == "" {
		requestID = response.Header.Get("request-id")
	}
	class := classifyHTTP(response.StatusCode, code, envelope.Error.Message+" "+envelope.Message)
	return &CallError{Class: class, Code: code, HTTPStatus: response.StatusCode, ProviderRequestID: requestID, RetryAfter: retryAfter(response.Header, now)}
}

func classifyHTTP(status int, code, message string) string {
	combined := strings.ToLower(code + " " + message)
	switch {
	case status == http.StatusTooManyRequests:
		return ClassRateLimited
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ClassAuthFailed
	case strings.Contains(combined, "context_length") || strings.Contains(combined, "context window") || strings.Contains(combined, "too many tokens"):
		return ClassContextTooLong
	case strings.Contains(combined, "content_filter") || strings.Contains(combined, "content filter") || strings.Contains(combined, "safety"):
		return ClassContentFiltered
	case status == http.StatusNotFound || strings.Contains(combined, "model_not_found") || strings.Contains(combined, "model unavailable"):
		return ClassModelUnavailable
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return ClassTimeout
	case status >= 500:
		return ClassProviderError
	case status >= 400:
		return ClassInvalidRequest
	default:
		return ClassProviderError
	}
}

func transportCallError(ctx context.Context, err error) error {
	var known *CallError
	if errors.As(err, &known) {
		return known
	}
	class := ClassProviderError
	if errors.Is(err, context.DeadlineExceeded) {
		class = ClassTimeout
	} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		class = ClassCancelled
	}
	var classified interface{ ProviderFailureClass() string }
	if errors.As(err, &classified) && classified.ProviderFailureClass() != "" {
		class = classified.ProviderFailureClass()
	}
	outcomeUnknown := true
	var state interface{ RequestMayHaveBeenSent() bool }
	if errors.As(err, &state) {
		outcomeUnknown = state.RequestMayHaveBeenSent()
	}
	return &CallError{Class: class, OutcomeUnknown: outcomeUnknown, Cause: err}
}

func parseCallError(err error, result Result) error {
	var known *CallError
	if errors.As(err, &known) {
		if known.ProviderRequestID == "" {
			known.ProviderRequestID = result.ProviderRequestID
		}
		return known
	}
	return &CallError{Class: ClassProviderError, ProviderRequestID: result.ProviderRequestID, Cause: err}
}

func ensureContentType(response *http.Response, expected string) error {
	value := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.HasPrefix(value, expected) {
		return fmt.Errorf("%w: unexpected content type", ErrResponseInvalid)
	}
	return nil
}
