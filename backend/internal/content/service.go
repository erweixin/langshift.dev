package content

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lites/backend/internal/event"
)

type Service struct {
	events     *event.Service
	ids        event.IDGenerator
	now        func() time.Time
	runTimeout time.Duration
}

type ServiceOptions struct {
	IDGenerator event.IDGenerator
	Now         func() time.Time
	RunTimeout  time.Duration
}

func NewService(events *event.Service, options ServiceOptions) *Service {
	ids := options.IDGenerator
	if ids == nil {
		ids = event.NewULIDGenerator(nil)
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	runTimeout := options.RunTimeout
	if runTimeout <= 0 {
		runTimeout = time.Duration(defaultRunTimeoutSeconds) * time.Second
	}
	return &Service{
		events:     events,
		ids:        ids,
		now:        now,
		runTimeout: runTimeout,
	}
}

func (s *Service) Start(ctx context.Context, request StartRequest) (StartResult, error) {
	if s == nil || s.events == nil {
		return StartResult{}, errMissingEvents
	}
	if s.ids == nil {
		return StartResult{}, errMissingIDs
	}
	input, err := normalizeInput(request.Input)
	if err != nil {
		return StartResult{}, err
	}
	if strings.TrimSpace(request.UserID) == "" {
		return StartResult{}, fmt.Errorf("%w: user_id is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return StartResult{}, fmt.Errorf("%w: idempotency key is required", ErrInvalidRequest)
	}

	runID, err := s.ids.NewID()
	if err != nil {
		return StartResult{}, fmt.Errorf("generate content generation run id: %w", err)
	}

	inputRef, err := json.Marshal(input)
	if err != nil {
		return StartResult{}, fmt.Errorf("marshal content generation input_ref: %w", err)
	}
	dueAt := s.now().Add(s.runTimeout).UTC()
	runPayload, err := json.Marshal(acceptedPayload{
		RunID:    runID,
		RunType:  RunTypeGeneration,
		InputRef: inputRef,
		DueAt:    dueAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("marshal RunAccepted payload: %w", err)
	}

	commandPayload, err := json.Marshal(jobPayload{RunID: runID, Input: input})
	if err != nil {
		return StartResult{}, fmt.Errorf("marshal content generation job payload: %w", err)
	}
	responseBody, err := json.Marshal(StartResult{
		RunID:  runID,
		Status: "queued",
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("marshal content generation response: %w", err)
	}
	hash, err := requestHash(input)
	if err != nil {
		return StartResult{}, err
	}

	result, err := s.events.Append(ctx, event.AppendRequest{
		Actor:  event.Actor{Kind: event.ActorUser},
		UserID: request.UserID,
		Idempotency: &event.Idempotency{
			Scope:       "POST /api/content-generation-runs",
			Key:         request.IdempotencyKey,
			RequestHash: hash,
		},
		IdempotencyResponse: &event.IdempotencyResponse{
			Status: 202,
			Body:   responseBody,
		},
		Events: []event.EventDraft{{
			Type:          "RunAccepted",
			SchemaVersion: 1,
			RunID:         runID,
			Payload:       runPayload,
		}},
		Commands: []event.CommandDraft{{
			Kind:          JobKindContentGeneration,
			SubjectUserID: request.UserID,
			Payload:       commandPayload,
		}},
	})
	if err != nil {
		return StartResult{}, err
	}

	var response StartResult
	if result.IdempotencyResponse != nil && len(result.IdempotencyResponse.Body) > 0 {
		if err := json.Unmarshal(result.IdempotencyResponse.Body, &response); err != nil {
			return StartResult{}, fmt.Errorf("parse content generation idempotency response: %w", err)
		}
	}
	if response.RunID == "" {
		response.RunID = runID
	}
	if response.Status == "" {
		response.Status = "queued"
	}
	response.Replayed = result.Replayed
	return response, nil
}

func normalizeInput(input GenerationInput) (GenerationInput, error) {
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.TaskTemplateID = strings.TrimSpace(input.TaskTemplateID)
	input.TargetStack = strings.TrimSpace(input.TargetStack)
	input.LevelBand = strings.TrimSpace(input.LevelBand)
	input.Title = strings.TrimSpace(input.Title)
	input.Judge = strings.TrimSpace(input.Judge)
	if input.Minutes == 0 {
		input.Minutes = 30
	}
	if input.Context == nil || len(strings.TrimSpace(string(input.Context))) == 0 {
		input.Context = json.RawMessage(`{}`)
	}

	switch {
	case input.TaskTemplateID == "":
		return GenerationInput{}, fmt.Errorf("%w: task_template_id is required", ErrInvalidRequest)
	case input.TargetStack == "":
		return GenerationInput{}, fmt.Errorf("%w: target_stack is required", ErrInvalidRequest)
	case input.LevelBand == "":
		return GenerationInput{}, fmt.Errorf("%w: level_band is required", ErrInvalidRequest)
	case input.Title == "":
		return GenerationInput{}, fmt.Errorf("%w: title is required", ErrInvalidRequest)
	case input.Judge == "":
		return GenerationInput{}, fmt.Errorf("%w: judge is required", ErrInvalidRequest)
	case input.Minutes < 5 || input.Minutes > 180:
		return GenerationInput{}, fmt.Errorf("%w: minutes must be between 5 and 180", ErrInvalidRequest)
	case !json.Valid(input.Context):
		return GenerationInput{}, fmt.Errorf("%w: context must be valid json", ErrInvalidRequest)
	}

	var contextObject map[string]any
	if err := json.Unmarshal(input.Context, &contextObject); err != nil || contextObject == nil {
		return GenerationInput{}, fmt.Errorf("%w: context must be a json object", ErrInvalidRequest)
	}
	normalizedContext, err := json.Marshal(contextObject)
	if err != nil {
		return GenerationInput{}, fmt.Errorf("normalize content generation context: %w", err)
	}
	input.Context = normalizedContext
	return input, nil
}
