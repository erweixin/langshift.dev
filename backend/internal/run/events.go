package run

import (
	"encoding/json"
	"fmt"
	"time"

	"lites/backend/internal/event"
)

type Fields map[string]any

type AcceptedEventRequest struct {
	RunID          string
	RunType        string
	InputRef       json.RawMessage
	DueAt          *time.Time
	ConversationID string
}

func AcceptedEvent(request AcceptedEventRequest) (event.EventDraft, error) {
	inputRef := request.InputRef
	if len(inputRef) == 0 {
		inputRef = json.RawMessage(`{}`)
	}
	if !json.Valid(inputRef) {
		return event.EventDraft{}, fmt.Errorf("%w: input_ref must be valid json", ErrInvalidPayload)
	}

	payload, err := json.Marshal(acceptedPayload{
		RunID:    request.RunID,
		RunType:  request.RunType,
		InputRef: inputRef,
		DueAt:    request.DueAt,
	})
	if err != nil {
		return event.EventDraft{}, fmt.Errorf("marshal RunAccepted payload: %w", err)
	}
	return event.EventDraft{
		Type:           EventRunAccepted,
		SchemaVersion:  1,
		RunID:          request.RunID,
		ConversationID: request.ConversationID,
		Payload:        payload,
	}, nil
}

func QueuedEvent(runID string, fields Fields) (event.EventDraft, error) {
	return eventWithFields(EventRunQueued, runID, fields)
}

func StartedEvent(runID string, attemptID string, fields Fields) (event.EventDraft, error) {
	merged := cloneFields(fields)
	if attemptID != "" {
		merged["attempt_id"] = attemptID
	}
	return eventWithFields(EventRunStarted, runID, merged)
}

func SucceededEvent(runID string, attemptKeys []string, fields Fields) (event.EventDraft, error) {
	merged := cloneFields(fields)
	if len(attemptKeys) > 0 {
		merged["attempt_keys"] = attemptKeys
	}
	return eventWithFields(EventRunSucceeded, runID, merged)
}

func FailedEvent(runID string, attemptKeys []string, code string, message string, fields Fields) (event.EventDraft, error) {
	merged := cloneFields(fields)
	if len(attemptKeys) > 0 {
		merged["attempt_keys"] = attemptKeys
	}
	merged["error"] = map[string]string{
		"code":    code,
		"message": message,
	}
	return eventWithFields(EventRunFailed, runID, merged)
}

func ExpiredEvent(runID string, code string, message string, fields Fields) (event.EventDraft, error) {
	merged := cloneFields(fields)
	merged["error"] = map[string]string{
		"code":    code,
		"message": message,
	}
	return eventWithFields(EventRunExpired, runID, merged)
}

func eventWithFields(eventType string, runID string, fields Fields) (event.EventDraft, error) {
	payload, err := payloadWithRunID(runID, fields)
	if err != nil {
		return event.EventDraft{}, err
	}
	return event.EventDraft{
		Type:          eventType,
		SchemaVersion: 1,
		RunID:         runID,
		Payload:       payload,
	}, nil
}

func payloadWithRunID(runID string, fields Fields) (json.RawMessage, error) {
	payload := cloneFields(fields)
	payload["run_id"] = runID
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal run event payload: %v", ErrInvalidPayload, err)
	}
	return encoded, nil
}

func cloneFields(fields Fields) Fields {
	merged := make(Fields, len(fields)+1)
	for key, value := range fields {
		merged[key] = value
	}
	return merged
}
