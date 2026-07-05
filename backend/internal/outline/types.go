package outline

import (
	"encoding/json"
	"errors"
	"time"

	"lites/backend/internal/contracts"
)

const (
	JobKindOutlineGeneration = "outline_generation"
	LLMSurfaceOutline        = "outline_generation"
	PromptVersionOutline     = "outline-generation-v0"
	RunTypeOutline           = "outline_gen"

	learningOutlineSchemaVersion = 1
	defaultRunTimeoutSeconds     = 15 * 60
	defaultWorkerID              = "outline-generation-worker"
	defaultIdleSleepMillis       = 1000
)

var (
	ErrInvalidRequest = errors.New("invalid outline generation request")
	ErrInvalidOutline = errors.New("invalid learning outline")
	ErrNotFound       = errors.New("outline not found")
	errMissingEvents  = errors.New("missing event service")
	errMissingIDs     = errors.New("missing id generator")
	errMissingStore   = errors.New("missing outline store")
)

type GenerationInput struct {
	Goal         string   `json:"goal"`
	CurrentLevel string   `json:"current_level"`
	TargetRole   string   `json:"target_role"`
	TargetStack  string   `json:"target_stack"`
	DailyMinutes int      `json:"daily_minutes,omitempty"`
	DurationDays int      `json:"duration_days,omitempty"`
	Preferences  []string `json:"preferences,omitempty"`
	Constraints  []string `json:"constraints,omitempty"`
	Background   string   `json:"background,omitempty"`
}

type StartRequest struct {
	UserID         string
	IdempotencyKey string
	Input          GenerationInput
}

type StartResult struct {
	Replayed  bool   `json:"-"`
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	OutlineID string `json:"outline_id,omitempty"`
}

type RunResult struct {
	RunID     string          `json:"run_id"`
	Status    string          `json:"status"`
	OutlineID string          `json:"outline_id,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
}

type SaveOutlineRequest struct {
	UserID           string
	RunID            string
	Input            GenerationInput
	Outline          contracts.LearningOutline
	LLMLedgerID      string
	SourceAttemptKey string
}

type OutlineRecord struct {
	UserID           string
	RunID            string
	Input            GenerationInput
	Outline          contracts.LearningOutline
	LLMLedgerID      string
	SourceAttemptKey string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type jobPayload struct {
	RunID string          `json:"run_id"`
	Input GenerationInput `json:"input"`
}
