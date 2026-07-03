package event

import (
	"encoding/json"
	"errors"
	"strings"
)

var (
	defaultIdempotencyBody  = json.RawMessage(`{}`)
	defaultIdempotencyState = httpStatusOK
)

func normalizeIdempotencyResponse(response *IdempotencyResponse) *IdempotencyResponse {
	if response == nil {
		return &IdempotencyResponse{
			Status: defaultIdempotencyState,
			Body:   append(json.RawMessage(nil), defaultIdempotencyBody...),
		}
	}
	status := response.Status
	if status == 0 {
		status = defaultIdempotencyState
	}
	body := response.Body
	if len(strings.TrimSpace(string(body))) == 0 {
		body = defaultIdempotencyBody
	}
	return &IdempotencyResponse{
		Status: status,
		Body:   append(json.RawMessage(nil), body...),
	}
}

func normalizeJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return append(json.RawMessage(nil), defaultIdempotencyBody...), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("invalid json")
	}
	return append(json.RawMessage(nil), raw...), nil
}
