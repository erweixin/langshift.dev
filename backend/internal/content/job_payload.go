package content

import (
	"encoding/json"
	"fmt"
	"strings"

	"lites/backend/internal/job"
)

type GenerationJobPayload struct {
	RunID string          `json:"run_id"`
	Input GenerationInput `json:"input"`
}

func NewGenerationJobPayload(runID string, input GenerationInput) (GenerationJobPayload, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return GenerationJobPayload{}, fmt.Errorf("%w: run_id is required", ErrInvalidRequest)
	}
	normalized, err := normalizeInput(input)
	if err != nil {
		return GenerationJobPayload{}, err
	}
	return GenerationJobPayload{
		RunID: runID,
		Input: normalized,
	}, nil
}

func MarshalGenerationJobPayload(runID string, input GenerationInput) (json.RawMessage, error) {
	payload, err := NewGenerationJobPayload(runID, input)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal content generation job payload: %w", err)
	}
	return encoded, nil
}

func DecodeGenerationJobPayload(claimed job.Job) (GenerationJobPayload, error) {
	var payload GenerationJobPayload
	if err := json.Unmarshal(claimed.Payload, &payload); err != nil {
		return GenerationJobPayload{}, fmt.Errorf("%w: parse job payload: %v", ErrInvalidRequest, err)
	}
	return NewGenerationJobPayload(payload.RunID, payload.Input)
}
