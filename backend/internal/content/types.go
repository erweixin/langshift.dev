package content

import (
	"encoding/json"
	"errors"
	"time"

	"lites/backend/internal/contracts"
)

const (
	JobKindContentGeneration = "content_generation"
	LLMSurfaceGeneration     = "lesson_generation"
	PromptVersionGeneration  = "content-generation-v0"
	RunTypeGeneration        = "lesson_gen"
	ReviewStatusAutoOK       = "auto_ok"

	contentArtifactSchemaVersion = 1
	contentArtifactVersion       = 1
	contentArtifactPromptVersion = 1
	defaultRunTimeoutSeconds     = 15 * 60
	defaultWorkerID              = "content-generation-worker"
	defaultIdleSleepMillis       = 1000
)

var (
	ErrInvalidRequest   = errors.New("invalid content generation request")
	ErrInvalidArtifact  = errors.New("invalid content artifact")
	ErrValidationFailed = errors.New("content validation failed")
	ErrNotFound         = errors.New("content not found")
	errMissingEvents    = errors.New("missing event service")
	errMissingQueue     = errors.New("missing job queue")
	errMissingIDs       = errors.New("missing id generator")
	errMissingStore     = errors.New("missing content artifact store")
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
	Replayed   bool   `json:"-"`
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	ContentKey string `json:"content_key,omitempty"`
	CacheHit   bool   `json:"cache_hit,omitempty"`
}

type RunResult struct {
	RunID      string          `json:"run_id"`
	Status     string          `json:"status"`
	ContentKey string          `json:"content_key,omitempty"`
	Error      json.RawMessage `json:"error,omitempty"`
}

type SaveArtifactRequest struct {
	Artifact           contracts.ContentArtifact
	LLMLedgerID        string
	SourceRunID        string
	SourceAttemptKey   string
	ReviewStatus       string
	ValidationAttempts int
}

type ArtifactRecord struct {
	Artifact           contracts.ContentArtifact
	ArtifactHash       string
	ReviewStatus       string
	ValidationAttempts int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type ValidationRequest struct {
	Input    GenerationInput
	Artifact contracts.ContentArtifact
}

type ValidationResult struct {
	Attempts int               `json:"attempts"`
	Issues   []ValidationIssue `json:"issues,omitempty"`
}

func (r ValidationResult) Passed() bool {
	return len(r.Issues) == 0
}

type ValidationIssue struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type ExerciseRuntimeRequest struct {
	Artifact contracts.ContentArtifact
}

type ExerciseRuntimeResult struct {
	Skipped         bool              `json:"skipped,omitempty"`
	ReferencePassed bool              `json:"reference_passed,omitempty"`
	StarterFailed   bool              `json:"starter_failed,omitempty"`
	Issues          []ValidationIssue `json:"issues,omitempty"`
}

type jobPayload struct {
	RunID string          `json:"run_id"`
	Input GenerationInput `json:"input"`
}
