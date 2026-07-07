package outline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lites/backend/internal/event"
	"lites/backend/internal/run"
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
		return StartResult{}, fmt.Errorf("generate outline generation run id: %w", err)
	}

	inputRef, err := json.Marshal(input)
	if err != nil {
		return StartResult{}, fmt.Errorf("marshal outline generation input_ref: %w", err)
	}
	dueAt := s.now().Add(s.runTimeout).UTC()
	runAccepted, err := run.AcceptedEvent(run.AcceptedEventRequest{
		RunID:    runID,
		RunType:  RunTypeOutline,
		InputRef: inputRef,
		DueAt:    &dueAt,
	})
	if err != nil {
		return StartResult{}, err
	}
	commandPayload, err := MarshalGenerationJobPayload(runID, input)
	if err != nil {
		return StartResult{}, err
	}
	responseBody, err := json.Marshal(StartResult{RunID: runID, Status: "queued"})
	if err != nil {
		return StartResult{}, fmt.Errorf("marshal outline generation response: %w", err)
	}
	hash, err := requestHash(input)
	if err != nil {
		return StartResult{}, err
	}

	result, err := s.events.Append(ctx, event.NewUserCommandAppend(event.UserCommandAppendRequest{
		UserID: request.UserID,
		Idempotency: event.Idempotency{
			Scope:       "POST /api/outline-generation-runs",
			Key:         request.IdempotencyKey,
			RequestHash: hash,
		},
		IdempotencyResponse: event.IdempotencyResponse{
			Status: 202,
			Body:   responseBody,
		},
		Events: []event.EventDraft{runAccepted},
		Commands: []event.CommandDraft{{
			Kind:          JobKindOutlineGeneration,
			SubjectUserID: request.UserID,
			Payload:       commandPayload,
		}},
	}))
	if err != nil {
		return StartResult{}, err
	}

	var response StartResult
	if result.IdempotencyResponse != nil && len(result.IdempotencyResponse.Body) > 0 {
		if err := json.Unmarshal(result.IdempotencyResponse.Body, &response); err != nil {
			return StartResult{}, fmt.Errorf("parse outline generation idempotency response: %w", err)
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
	input.Goal = strings.TrimSpace(input.Goal)
	input.CurrentLevel = strings.TrimSpace(input.CurrentLevel)
	input.TargetRole = strings.TrimSpace(input.TargetRole)
	input.TargetStack = strings.TrimSpace(input.TargetStack)
	input.Background = strings.TrimSpace(input.Background)
	input.Preferences = cleanStrings(input.Preferences)
	input.Constraints = cleanStrings(input.Constraints)
	if input.DailyMinutes == 0 {
		input.DailyMinutes = 30
	}
	if input.DurationDays == 0 {
		input.DurationDays = 14
	}

	switch {
	case input.Goal == "":
		return GenerationInput{}, fmt.Errorf("%w: goal is required", ErrInvalidRequest)
	case input.CurrentLevel == "":
		return GenerationInput{}, fmt.Errorf("%w: current_level is required", ErrInvalidRequest)
	case input.TargetRole == "":
		return GenerationInput{}, fmt.Errorf("%w: target_role is required", ErrInvalidRequest)
	case input.TargetStack == "":
		return GenerationInput{}, fmt.Errorf("%w: target_stack is required", ErrInvalidRequest)
	case input.DailyMinutes < 5 || input.DailyMinutes > 240:
		return GenerationInput{}, fmt.Errorf("%w: daily_minutes must be between 5 and 240", ErrInvalidRequest)
	case input.DurationDays < 1 || input.DurationDays > 60:
		return GenerationInput{}, fmt.Errorf("%w: duration_days must be between 1 and 60", ErrInvalidRequest)
	}
	return input, nil
}

func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}
