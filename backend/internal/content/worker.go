package content

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lites/backend/internal/agentcore"
	"lites/backend/internal/contracts"
	"lites/backend/internal/event"
	"lites/backend/internal/llm"
	"lites/backend/internal/run"
)

type Queue = agentcore.Queue
type LLM = agentcore.LLM

type ArtifactWriter interface {
	SaveArtifact(ctx context.Context, request SaveArtifactRequest) (ArtifactRecord, error)
	SaveArtifactEffect(request SaveArtifactRequest) (event.TxEffect, ArtifactRecord, error)
}

type Worker struct {
	core *agentcore.Worker
}

type WorkerOptions struct {
	IDGenerator event.IDGenerator
	Validator   ArtifactValidator
	WorkerID    string
	IdleSleep   time.Duration
}

func NewWorker(queue Queue, events *event.Service, llmClient LLM, artifacts ArtifactWriter, options WorkerOptions) *Worker {
	validator := options.Validator
	if validator == nil {
		validator = NewStaticValidator(StaticValidatorOptions{})
	}
	workerID := strings.TrimSpace(options.WorkerID)
	if workerID == "" {
		workerID = defaultWorkerID
	}
	idleSleep := options.IdleSleep
	if idleSleep <= 0 {
		idleSleep = time.Duration(defaultIdleSleepMillis) * time.Millisecond
	}
	handler := contentGenerationHandler{
		artifacts: artifacts,
		validator: validator,
	}
	return &Worker{
		core: agentcore.NewWorker(queue, events, llmClient, handler, agentcore.WorkerOptions{
			IDGenerator: options.IDGenerator,
			WorkerID:    workerID,
			IdleSleep:   idleSleep,
		}),
	}
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.core == nil {
		return
	}
	w.core.Run(ctx)
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if w == nil || w.core == nil {
		return false, errMissingQueue
	}
	return w.core.ProcessOne(ctx)
}

type contentGenerationHandler struct {
	artifacts ArtifactWriter
	validator ArtifactValidator
}

func (h contentGenerationHandler) JobKinds() []string {
	return []string{JobKindContentGeneration}
}

func (h contentGenerationHandler) BuildLLMRequest(_ context.Context, task agentcore.JobContext) (llm.Request, error) {
	payload, err := DecodeGenerationJobPayload(task.Job)
	if err != nil {
		return llm.Request{}, err
	}
	if strings.TrimSpace(task.Job.SubjectUserID) == "" {
		return llm.Request{}, fmt.Errorf("%w: subject_user_id is required", ErrInvalidRequest)
	}
	return llm.Request{
		UserID:          task.Job.SubjectUserID,
		RunID:           payload.RunID,
		Surface:         LLMSurfaceGeneration,
		AttemptKey:      task.AttemptKey,
		PromptVersion:   PromptVersionGeneration,
		ContextManifest: contextManifest(payload),
		Messages:        generationMessages(payload.Input),
		JSONMode:        true,
		SchemaName:      "content_artifact",
	}, nil
}

func (h contentGenerationHandler) BuildAppendRequest(ctx context.Context, completion agentcore.Completion) (agentcore.CompletionAppend, error) {
	if err := h.validateCompletionBuilder(); err != nil {
		return agentcore.CompletionAppend{}, err
	}

	payload, err := DecodeGenerationJobPayload(completion.Job)
	if err != nil {
		return agentcore.CompletionAppend{}, err
	}
	runID := payload.RunID
	events, err := startedRunEvents(runID, completion.AttemptKey)
	if err != nil {
		return agentcore.CompletionAppend{}, err
	}

	if completion.Err != nil {
		return failedCompletionAppend(completion, runID, events, "llm_error", completion.Err.Error(), nil)
	}

	artifact, err := buildGeneratedArtifact(payload.Input, completion.Response, time.Now().UTC())
	if err != nil {
		return failedCompletionAppend(completion, runID, events, "invalid_content_artifact", err.Error(), run.Fields{
			"llm_ledger_id": completion.Response.LedgerID,
		})
	}

	validation, err := h.validator.Validate(ctx, ValidationRequest{
		Input:    payload.Input,
		Artifact: artifact,
	})
	if err != nil {
		return agentcore.CompletionAppend{}, err
	}
	if !validation.Passed() {
		return failedCompletionAppend(completion, runID, events, "content_validation_failed", summarizeValidationIssues(validation.Issues), run.Fields{
			"llm_ledger_id": completion.Response.LedgerID,
			"validation":    validation,
		})
	}

	return h.succeededCompletionAppend(completion, payload, artifact, validation, events)
}

func (h contentGenerationHandler) validateCompletionBuilder() error {
	if h.artifacts == nil {
		return errMissingStore
	}
	if h.validator == nil {
		return fmt.Errorf("%w: validator is required", ErrInvalidRequest)
	}
	return nil
}

func startedRunEvents(runID string, attemptKey string) ([]event.EventDraft, error) {
	runQueued, err := run.QueuedEvent(runID, nil)
	if err != nil {
		return nil, err
	}
	runStarted, err := run.StartedEvent(runID, attemptKey, nil)
	if err != nil {
		return nil, err
	}
	return []event.EventDraft{runQueued, runStarted}, nil
}

func failedCompletionAppend(completion agentcore.Completion, runID string, events []event.EventDraft, code string, message string, fields run.Fields) (agentcore.CompletionAppend, error) {
	runFailed, err := run.FailedEvent(runID, []string{completion.AttemptKey}, code, truncateMessage(message), fields)
	if err != nil {
		return agentcore.CompletionAppend{}, err
	}
	return newCompletionAppend(completion, runID, append(events, runFailed), nil), nil
}

func (h contentGenerationHandler) succeededCompletionAppend(completion agentcore.Completion, payload GenerationJobPayload, artifact contracts.ContentArtifact, validation ValidationResult, events []event.EventDraft) (agentcore.CompletionAppend, error) {
	if validation.Attempts <= 0 {
		validation.Attempts = 1
	}
	if artifact.Meta != nil {
		artifact.Meta.ValidationAttempts = validation.Attempts
	}
	saveEffect, saved, err := h.artifacts.SaveArtifactEffect(SaveArtifactRequest{
		Artifact:           artifact,
		LLMLedgerID:        completion.Response.LedgerID,
		SourceRunID:        payload.RunID,
		SourceAttemptKey:   completion.AttemptKey,
		ReviewStatus:       ReviewStatusAutoOK,
		ValidationAttempts: validation.Attempts,
	})
	if err != nil {
		return agentcore.CompletionAppend{}, err
	}
	runSucceeded, err := run.SucceededEvent(payload.RunID, []string{completion.AttemptKey}, run.Fields{
		"llm_ledger_id":       completion.Response.LedgerID,
		"content_key":         saved.Artifact.ContentKey,
		"artifact_hash":       saved.ArtifactHash,
		"review_status":       saved.ReviewStatus,
		"validation_attempts": saved.ValidationAttempts,
		"content_length":      len(completion.Response.Content),
	})
	if err != nil {
		return agentcore.CompletionAppend{}, err
	}
	return newCompletionAppend(completion, payload.RunID, append(events, runSucceeded), []event.TxEffect{saveEffect}), nil
}

func newCompletionAppend(completion agentcore.Completion, runID string, events []event.EventDraft, effects []event.TxEffect) agentcore.CompletionAppend {
	return agentcore.CompletionAppend{
		UserID:             completion.Request.UserID,
		RunID:              runID,
		ExpectedRunVersion: agentcore.ExpectedRunVersion(0),
		Events:             events,
		Effects:            effects,
	}
}

func contextManifest(payload GenerationJobPayload) json.RawMessage {
	return mustJSON(map[string]any{
		"run_id": runIDOrUnknown(payload.RunID),
		"input":  payload.Input,
	})
}

func generationMessages(input GenerationInput) []llm.Message {
	body := fmt.Sprintf(`Generate one ContentArtifact JSON object for this learning task.

Task:
- task_id: %s
- task_template_id: %s
- target_stack: %s
- level_band: %s
- title: %s
- judge: %s
- minutes: %d

Return only valid JSON matching this exact shape:
{
  "lesson": {
    "title": "string",
    "minutes": number,
    "judge": "string",
    "why": "string",
    "sections": [
      {"id": "section_1", "title": "string", "body_md": "markdown"}
    ],
    "coach_note": "string"
  },
  "exercise": {
    "language": "javascript",
    "starter_code": "string",
    "reference_solution": "string",
    "harness_version": 1,
    "tests": [
      {"id": "case_1", "call": "string", "expect": "any JSON value", "judge": true, "label": "string"}
    ]
  }
}

Do not include user-specific personal data. Do not include markdown fences. The server will fill content_key, schema_version, task_template_id, target_stack, level_band, content_version, prompt_version, and meta.`,
		input.TaskID,
		input.TaskTemplateID,
		input.TargetStack,
		input.LevelBand,
		input.Title,
		input.Judge,
		input.Minutes,
	)
	return []llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: "You generate structured learning content for Lites. Do not include personal data. Return only JSON.",
		},
		{Role: llm.RoleUser, Content: body},
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func runIDOrUnknown(runID string) string {
	if runID == "" {
		return "unknown"
	}
	return runID
}

func truncateMessage(message string) string {
	const maxBytes = 2048
	if len(message) <= maxBytes {
		return message
	}
	return message[:maxBytes]
}

func summarizeValidationIssues(issues []ValidationIssue) string {
	if len(issues) == 0 {
		return ErrValidationFailed.Error()
	}
	first := issues[0]
	message := first.Message
	if first.Field != "" {
		message = first.Field + ": " + message
	}
	if len(issues) == 1 {
		return message
	}
	return fmt.Sprintf("%s; %d more validation issue(s)", message, len(issues)-1)
}
