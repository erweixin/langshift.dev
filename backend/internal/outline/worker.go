package outline

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

type OutlineWriter interface {
	SaveOutline(ctx context.Context, request SaveOutlineRequest) (OutlineRecord, error)
	SaveOutlineEffect(request SaveOutlineRequest) (event.TxEffect, OutlineRecord, error)
}

type Worker struct {
	core *agentcore.Worker
}

type WorkerOptions struct {
	IDGenerator event.IDGenerator
	WorkerID    string
	IdleSleep   time.Duration
}

func NewWorker(queue Queue, events *event.Service, llmClient LLM, outlines OutlineWriter, options WorkerOptions) *Worker {
	workerID := strings.TrimSpace(options.WorkerID)
	if workerID == "" {
		workerID = defaultWorkerID
	}
	idleSleep := options.IdleSleep
	if idleSleep <= 0 {
		idleSleep = time.Duration(defaultIdleSleepMillis) * time.Millisecond
	}
	handler := outlineGenerationHandler{outlines: outlines}
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
		return false, errMissingStore
	}
	return w.core.ProcessOne(ctx)
}

type outlineGenerationHandler struct {
	outlines OutlineWriter
}

func (h outlineGenerationHandler) JobKinds() []string {
	return []string{JobKindOutlineGeneration}
}

func (h outlineGenerationHandler) BuildLLMRequest(_ context.Context, task agentcore.JobContext) (llm.Request, error) {
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
		Surface:         LLMSurfaceOutline,
		AttemptKey:      task.AttemptKey,
		PromptVersion:   PromptVersionOutline,
		ContextManifest: contextManifest(payload),
		Messages:        generationMessages(payload.Input),
		JSONMode:        true,
		SchemaName:      "learning_outline",
	}, nil
}

func (h outlineGenerationHandler) BuildAppendRequest(_ context.Context, completion agentcore.Completion) (agentcore.CompletionAppend, error) {
	if h.outlines == nil {
		return agentcore.CompletionAppend{}, errMissingStore
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

	generated, err := buildGeneratedOutline(payload.Input, runID, completion.Response, time.Now().UTC())
	if err != nil {
		return failedCompletionAppend(completion, runID, events, "invalid_learning_outline", err.Error(), run.Fields{
			"llm_ledger_id": completion.Response.LedgerID,
		})
	}

	return h.succeededCompletionAppend(completion, payload, generated, events)
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

func (h outlineGenerationHandler) succeededCompletionAppend(completion agentcore.Completion, payload GenerationJobPayload, generated contracts.LearningOutline, events []event.EventDraft) (agentcore.CompletionAppend, error) {
	saveEffect, saved, err := h.outlines.SaveOutlineEffect(SaveOutlineRequest{
		UserID:           completion.Request.UserID,
		RunID:            payload.RunID,
		Input:            payload.Input,
		Outline:          generated,
		LLMLedgerID:      completion.Response.LedgerID,
		SourceAttemptKey: completion.AttemptKey,
	})
	if err != nil {
		return agentcore.CompletionAppend{}, err
	}
	runSucceeded, err := run.SucceededEvent(payload.RunID, []string{completion.AttemptKey}, run.Fields{
		"llm_ledger_id": completion.Response.LedgerID,
		"outline_id":    saved.Outline.OutlineID,
		"task_count":    len(saved.Outline.Tasks),
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
	body := fmt.Sprintf(`Generate one personalized LearningOutline JSON object.

User request:
- goal: %s
- current_level: %s
- target_role: %s
- target_stack: %s
- daily_minutes: %d
- duration_days: %d
- preferences: %s
- constraints: %s
- background: %s

Rules:
- Use the user information only to choose the roadmap and TaskSpecs.
- Do not generate lesson body content here.
- Each task must be a TaskSpec that can later drive reusable content generation.
- Keep task_template_id stable and readable, such as fe2agent-d01.
- Return only valid JSON matching this shape:
{
  "goal": "string",
  "target_role": "string",
  "target_stack": "string",
  "level_band": "beginner|default|advanced",
  "duration_days": number,
  "daily_minutes": number,
  "summary": "string",
  "stages": [
    {"id": "stage_1", "title": "string", "goal": "string", "days": [1, 2]}
  ],
  "tasks": [
    {
      "day_index": 1,
      "task_template_id": "string",
      "target_stack": "string",
      "level_band": "string",
      "title": "string",
      "judge": "specific observable judgment point",
      "minutes": 30
    }
  ]
}

The server will fill schema_version, outline_id, and meta.`,
		input.Goal,
		input.CurrentLevel,
		input.TargetRole,
		input.TargetStack,
		input.DailyMinutes,
		input.DurationDays,
		strings.Join(input.Preferences, "; "),
		strings.Join(input.Constraints, "; "),
		input.Background,
	)
	return []llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: "You generate personalized learning roadmaps for Lites. Return only JSON. Do not write lesson body content.",
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
