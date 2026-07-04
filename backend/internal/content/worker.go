package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/run"
)

type Queue interface {
	Claim(ctx context.Context, kinds []string, workerID string) (job.Job, event.JobFence, error)
	Fail(ctx context.Context, fence event.JobFence, cause error) error
}

type LLM interface {
	Complete(ctx context.Context, req llm.Request) (llm.Response, error)
}

type ArtifactWriter interface {
	SaveArtifact(ctx context.Context, request SaveArtifactRequest) (ArtifactRecord, error)
}

type Worker struct {
	queue     Queue
	events    *event.Service
	llm       LLM
	artifacts ArtifactWriter
	ids       event.IDGenerator
	workerID  string
	idleSleep time.Duration
}

type WorkerOptions struct {
	IDGenerator event.IDGenerator
	WorkerID    string
	IdleSleep   time.Duration
}

func NewWorker(queue Queue, events *event.Service, llmClient LLM, artifacts ArtifactWriter, options WorkerOptions) *Worker {
	ids := options.IDGenerator
	if ids == nil {
		ids = event.NewULIDGenerator(nil)
	}
	workerID := strings.TrimSpace(options.WorkerID)
	if workerID == "" {
		workerID = defaultWorkerID
	}
	idleSleep := options.IdleSleep
	if idleSleep <= 0 {
		idleSleep = time.Duration(defaultIdleSleepMillis) * time.Millisecond
	}
	return &Worker{
		queue:     queue,
		events:    events,
		llm:       llmClient,
		artifacts: artifacts,
		ids:       ids,
		workerID:  workerID,
		idleSleep: idleSleep,
	}
}

func (w *Worker) Run(ctx context.Context) {
	for {
		processed, err := w.ProcessOne(ctx)
		if err != nil {
			slog.Error("content generation worker iteration failed", "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		if processed {
			continue
		}

		timer := time.NewTimer(w.idleSleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if err := w.validate(); err != nil {
		return false, err
	}

	claimed, fence, err := w.queue.Claim(ctx, []string{JobKindContentGeneration}, w.workerID)
	if err != nil {
		if errors.Is(err, job.ErrNoJobAvailable) {
			return false, nil
		}
		return false, err
	}

	payload, err := parseJobPayload(claimed)
	if err != nil {
		return true, w.failClaimedJob(ctx, fence, err)
	}
	if strings.TrimSpace(claimed.SubjectUserID) == "" {
		return true, w.failClaimedJob(ctx, fence, fmt.Errorf("%w: subject_user_id is required", ErrInvalidRequest))
	}

	attemptKey, err := w.ids.NewID()
	if err != nil {
		return true, w.failClaimedJob(ctx, fence, fmt.Errorf("generate content generation attempt key: %w", err))
	}

	response, llmErr := w.llm.Complete(ctx, llm.Request{
		UserID:          claimed.SubjectUserID,
		RunID:           payload.RunID,
		Surface:         LLMSurfaceGeneration,
		AttemptKey:      attemptKey,
		PromptVersion:   PromptVersionGeneration,
		ContextManifest: contextManifest(payload),
		Messages:        generationMessages(payload.Input),
		JSONMode:        true,
		SchemaName:      "content_artifact",
	})

	appendErr := w.appendResult(ctx, claimed.SubjectUserID, fence, payload, attemptKey, response, llmErr)
	if appendErr != nil {
		if errors.Is(appendErr, event.ErrRunVersionConflict) || errors.Is(appendErr, run.ErrInvalidTransition) {
			if ackErr := w.ackStaleJob(ctx, claimed.SubjectUserID, fence); ackErr != nil {
				return true, fmt.Errorf("ack stale content generation job: %w", ackErr)
			}
			return true, nil
		}
		if failErr := w.queue.Fail(ctx, fence, appendErr); failErr != nil {
			return true, fmt.Errorf("fail content generation job after append error: %w", failErr)
		}
		return true, appendErr
	}
	return true, nil
}

func (w *Worker) appendResult(ctx context.Context, userID string, fence event.JobFence, payload jobPayload, attemptKey string, response llm.Response, llmErr error) error {
	runID := payload.RunID
	events := []event.EventDraft{
		runEvent(run.EventRunQueued, runID, json.RawMessage(`{}`)),
		runEvent(run.EventRunStarted, runID, mustJSON(map[string]any{
			"run_id":     runID,
			"attempt_id": attemptKey,
		})),
	}
	if llmErr != nil {
		events = append(events, runEvent(run.EventRunFailed, runID, mustJSON(map[string]any{
			"run_id":       runID,
			"attempt_keys": []string{attemptKey},
			"error": map[string]string{
				"code":    "llm_error",
				"message": truncateMessage(llmErr.Error()),
			},
		})))
	} else {
		artifact, err := buildGeneratedArtifact(payload.Input, response, time.Now().UTC())
		if err != nil {
			events = append(events, runEvent(run.EventRunFailed, runID, mustJSON(map[string]any{
				"run_id":        runID,
				"attempt_keys":  []string{attemptKey},
				"llm_ledger_id": response.LedgerID,
				"error": map[string]string{
					"code":    "invalid_content_artifact",
					"message": truncateMessage(err.Error()),
				},
			})))
		} else {
			saved, err := w.artifacts.SaveArtifact(ctx, SaveArtifactRequest{
				Artifact:         artifact,
				LLMLedgerID:      response.LedgerID,
				SourceRunID:      runID,
				SourceAttemptKey: attemptKey,
				ReviewStatus:     ReviewStatusAutoOK,
			})
			if err != nil {
				return err
			}
			events = append(events, runEvent(run.EventRunSucceeded, runID, mustJSON(map[string]any{
				"run_id":         runID,
				"attempt_keys":   []string{attemptKey},
				"llm_ledger_id":  response.LedgerID,
				"content_key":    saved.Artifact.ContentKey,
				"artifact_hash":  saved.ArtifactHash,
				"content_length": len(response.Content),
			})))
		}
	}

	_, err := w.events.Append(ctx, event.AppendRequest{
		Actor:  event.Actor{Kind: event.ActorWorker, ID: w.workerID},
		UserID: userID,
		JobFence: &event.JobFence{
			JobID:      fence.JobID,
			LeaseToken: fence.LeaseToken,
		},
		Aggregate: &event.RunAggregate{
			RunID:           runID,
			ExpectedVersion: 0,
		},
		Events: events,
	})
	return err
}

func (w *Worker) ackStaleJob(ctx context.Context, userID string, fence event.JobFence) error {
	_, err := w.events.Append(ctx, event.AppendRequest{
		Actor:  event.Actor{Kind: event.ActorWorker, ID: w.workerID},
		UserID: userID,
		JobFence: &event.JobFence{
			JobID:      fence.JobID,
			LeaseToken: fence.LeaseToken,
		},
	})
	return err
}

func (w *Worker) failClaimedJob(ctx context.Context, fence event.JobFence, cause error) error {
	if err := w.queue.Fail(ctx, fence, cause); err != nil {
		return fmt.Errorf("fail invalid content generation job: %w", err)
	}
	return cause
}

func (w *Worker) validate() error {
	if w == nil || w.queue == nil {
		return errMissingQueue
	}
	if w.events == nil {
		return errMissingEvents
	}
	if w.llm == nil {
		return errMissingLLM
	}
	if w.artifacts == nil {
		return errMissingStore
	}
	if w.ids == nil {
		return errMissingIDs
	}
	if strings.TrimSpace(w.workerID) == "" {
		return fmt.Errorf("%w: worker_id is required", ErrInvalidRequest)
	}
	return nil
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
