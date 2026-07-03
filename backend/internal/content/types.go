package content

import (
	"encoding/json"
	"errors"
)

const (
	JobKindContentGeneration = "content_generation"
	LLMSurfaceGeneration     = "lesson_generation"
	PromptVersionGeneration  = "content-generation-v0"
	RunTypeGeneration        = "lesson_gen"

	defaultRunTimeoutSeconds = 15 * 60
	defaultWorkerID          = "content-generation-worker"
	defaultIdleSleepMillis   = 1000
)

var (
	ErrInvalidRequest = errors.New("invalid content generation request")
	errMissingEvents  = errors.New("missing event service")
	errMissingQueue   = errors.New("missing job queue")
	errMissingLLM     = errors.New("missing llm client")
	errMissingIDs     = errors.New("missing id generator")
)

type GenerationInput struct {
	TaskID         string          `json:"task_id,omitempty"`
	TaskTemplateID string          `json:"task_template_id"`
	TargetStack    string          `json:"target_stack"`
	LevelBand      string          `json:"level_band"`
	Title          string          `json:"title"`
	Judge          string          `json:"judge"`
	Minutes        int             `json:"minutes,omitempty"`
	Context        json.RawMessage `json:"context,omitempty"`
}

type StartRequest struct {
	UserID         string
	IdempotencyKey string
	Input          GenerationInput
}

type StartResult struct {
	Replayed bool   `json:"-"`
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
}

type acceptedPayload struct {
	RunID    string          `json:"run_id"`
	RunType  string          `json:"run_type"`
	InputRef json.RawMessage `json:"input_ref"`
	DueAt    string          `json:"due_at,omitempty"`
}

type jobPayload struct {
	RunID string          `json:"run_id"`
	Input GenerationInput `json:"input"`
}
