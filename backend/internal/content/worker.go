package content

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lites/backend/internal/agentcore"
	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/run"
)

type Queue = agentcore.Queue
type LLM = agentcore.LLM

type ArtifactWriter interface {
	SaveArtifact(ctx context.Context, request SaveArtifactRequest) (ArtifactRecord, error)
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
	payload, err := parseJobPayload(task.Job)
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
	if h.artifacts == nil {
		return agentcore.CompletionAppend{}, errMissingStore
	}
	if h.validator == nil {
		return agentcore.CompletionAppend{}, fmt.Errorf("%w: validator is required", ErrInvalidRequest)
	}

	payload, err := parseJobPayload(completion.Job)
	if err != nil {
		return agentcore.CompletionAppend{}, err
	}
	runID := payload.RunID
	events := []event.EventDraft{
		runEvent(run.EventRunQueued, runID, json.RawMessage(`{}`)),
		runEvent(run.EventRunStarted, runID, mustJSON(map[string]any{
			"run_id":     runID,
			"attempt_id": completion.AttemptKey,
		})),
	}
	if completion.Err != nil {
		events = append(events, runEvent(run.EventRunFailed, runID, mustJSON(map[string]any{
			"run_id":       runID,
			"attempt_keys": []string{completion.AttemptKey},
			"error": map[string]string{
				"code":    "llm_error",
				"message": truncateMessage(completion.Err.Error()),
			},
		})))
	} else {
		artifact, err := buildGeneratedArtifact(payload.Input, completion.Response, time.Now().UTC())
		if err != nil {
			events = append(events, runEvent(run.EventRunFailed, runID, mustJSON(map[string]any{
				"run_id":        runID,
				"attempt_keys":  []string{completion.AttemptKey},
				"llm_ledger_id": completion.Response.LedgerID,
				"error": map[string]string{
					"code":    "invalid_content_artifact",
					"message": truncateMessage(err.Error()),
				},
			})))
		} else {
			validation, err := h.validator.Validate(ctx, ValidationRequest{
				Input:    payload.Input,
				Artifact: artifact,
			})
			if err != nil {
				return agentcore.CompletionAppend{}, err
			}
			if !validation.Passed() {
				events = append(events, runEvent(run.EventRunFailed, runID, mustJSON(map[string]any{
					"run_id":        runID,
					"attempt_keys":  []string{completion.AttemptKey},
					"llm_ledger_id": completion.Response.LedgerID,
					"validation":    validation,
					"error": map[string]string{
						"code":    "content_validation_failed",
						"message": truncateMessage(summarizeValidationIssues(validation.Issues)),
					},
				})))
			} else {
				if validation.Attempts <= 0 {
					validation.Attempts = 1
				}
				if artifact.Meta != nil {
					artifact.Meta.ValidationAttempts = validation.Attempts
				}
				saved, err := h.artifacts.SaveArtifact(ctx, SaveArtifactRequest{
					Artifact:           artifact,
					LLMLedgerID:        completion.Response.LedgerID,
					SourceRunID:        runID,
					SourceAttemptKey:   completion.AttemptKey,
					ReviewStatus:       ReviewStatusAutoOK,
					ValidationAttempts: validation.Attempts,
				})
				if err != nil {
					return agentcore.CompletionAppend{}, err
				}
				events = append(events, runEvent(run.EventRunSucceeded, runID, mustJSON(map[string]any{
					"run_id":              runID,
					"attempt_keys":        []string{completion.AttemptKey},
					"llm_ledger_id":       completion.Response.LedgerID,
					"content_key":         saved.Artifact.ContentKey,
					"artifact_hash":       saved.ArtifactHash,
					"review_status":       saved.ReviewStatus,
					"validation_attempts": saved.ValidationAttempts,
					"content_length":      len(completion.Response.Content),
				})))
			}
		}
	}

	return agentcore.CompletionAppend{
		UserID:             completion.Request.UserID,
		RunID:              runID,
		ExpectedRunVersion: agentcore.ExpectedRunVersion(0),
		Events:             events,
	}, nil
}

func parseJobPayload(claimed job.Job) (jobPayload, error) {
	var payload jobPayload
	if err := json.Unmarshal(claimed.Payload, &payload); err != nil {
		return jobPayload{}, fmt.Errorf("%w: parse job payload: %v", ErrInvalidRequest, err)
	}
	if strings.TrimSpace(payload.RunID) == "" {
		return jobPayload{}, fmt.Errorf("%w: run_id is required", ErrInvalidRequest)
	}
	input, err := normalizeInput(payload.Input)
	if err != nil {
		return jobPayload{}, err
	}
	payload.Input = input
	return payload, nil
}

func runEvent(eventType string, runID string, payload json.RawMessage) event.EventDraft {
	return event.EventDraft{
		Type:          eventType,
		SchemaVersion: 1,
		RunID:         runID,
		Payload:       payload,
	}
}

func contextManifest(payload jobPayload) json.RawMessage {
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
