package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"lites/backend/internal/event"
)

type Reducer struct{}

func NewReducer() Reducer {
	return Reducer{}
}

func (Reducer) Apply(ctx context.Context, tx pgx.Tx, stored event.StoredEvent) error {
	switch stored.Type {
	case EventRunAccepted:
		return applyRunAccepted(ctx, tx, stored)
	case EventRunQueued:
		return applyRunTransition(ctx, tx, stored, StatusQueued)
	case EventRunStarted:
		return applyRunTransition(ctx, tx, stored, StatusExecuting)
	case EventRunSucceeded:
		return applyRunTransition(ctx, tx, stored, StatusSucceeded)
	case EventRunFailed:
		return applyRunTransition(ctx, tx, stored, StatusFailed)
	case EventRunExpired:
		return applyRunTransition(ctx, tx, stored, StatusExpired)
	default:
		return nil
	}
}

type acceptedPayload struct {
	RunID    string          `json:"run_id"`
	RunType  string          `json:"run_type"`
	InputRef json.RawMessage `json:"input_ref"`
	DueAt    *time.Time      `json:"due_at"`
}

type terminalPayload struct {
	Error json.RawMessage `json:"error"`
}

func applyRunAccepted(ctx context.Context, tx pgx.Tx, stored event.StoredEvent) error {
	payload, err := parseAcceptedPayload(stored)
	if err != nil {
		return err
	}
	runID := firstNonEmpty(stored.RunID, payload.RunID)
	if runID == "" {
		return fmt.Errorf("%w: run_id is required", ErrInvalidPayload)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO agent_runs (
			run_id, user_id, conversation_id, run_type, status,
			run_version, input_ref, due_at
		)
		VALUES (
			$1, $2, nullif($3, ''), $4, 'accepted',
			0, $5::jsonb, $6
		)
		ON CONFLICT (run_id) DO NOTHING
	`, runID, stored.UserID, stored.ConversationID, payload.RunType, string(payload.InputRef), payload.DueAt)
	if err != nil {
		return fmt.Errorf("apply RunAccepted: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	current, _, err := readRunState(ctx, tx, stored.UserID, runID)
	if err != nil {
		return err
	}
	if current == StatusAccepted {
		return nil
	}
	return fmt.Errorf("%w: RunAccepted found existing run in %s", ErrInvalidTransition, current)
}

func applyRunTransition(ctx context.Context, tx pgx.Tx, stored event.StoredEvent, target string) error {
	runID := stored.RunID
	if runID == "" {
		return fmt.Errorf("%w: run_id is required", ErrInvalidPayload)
	}
	current, version, err := readRunState(ctx, tx, stored.UserID, runID)
	if err != nil {
		return err
	}
	if current == target {
		return nil
	}
	if version == 0 {
		return fmt.Errorf("%w: %s -> %s", ErrMissingRunCAS, current, target)
	}
	if !CanTransition(current, target) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current, target)
	}

	errorJSON, err := terminalErrorJSON(stored)
	if err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE agent_runs
		SET status = $3,
			started_at = CASE
				WHEN $3 = 'executing' THEN COALESCE(started_at, now())
				ELSE started_at
			END,
			finished_at = CASE
				WHEN $3 IN ('succeeded', 'failed', 'expired', 'cancelled') THEN COALESCE(finished_at, now())
				ELSE finished_at
			END,
			error = CASE
				WHEN $4 = '' THEN error
				ELSE $4::jsonb
			END,
			updated_at = now()
		WHERE run_id = $1
			AND user_id = $2
			AND status = $5
			AND run_version > 0
	`, runID, stored.UserID, target, errorJSON, current)
	if err != nil {
		return fmt.Errorf("apply %s: %w", stored.Type, err)
	}
	if tag.RowsAffected() != 1 {
		nextCurrent, _, readErr := readRunState(ctx, tx, stored.UserID, runID)
		if readErr != nil {
			return readErr
		}
		if nextCurrent == target {
			return nil
		}
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, nextCurrent, target)
	}
	return nil
}

func parseAcceptedPayload(stored event.StoredEvent) (acceptedPayload, error) {
	var payload acceptedPayload
	if len(stored.Payload) > 0 {
		if err := json.Unmarshal(stored.Payload, &payload); err != nil {
			return acceptedPayload{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}
	if payload.RunType == "" {
		return acceptedPayload{}, fmt.Errorf("%w: run_type is required", ErrInvalidPayload)
	}
	if len(payload.InputRef) == 0 {
		payload.InputRef = json.RawMessage(`{}`)
	}
	if !json.Valid(payload.InputRef) {
		return acceptedPayload{}, fmt.Errorf("%w: input_ref must be valid json", ErrInvalidPayload)
	}
	return payload, nil
}

func terminalErrorJSON(stored event.StoredEvent) (string, error) {
	if stored.Type != EventRunFailed && stored.Type != EventRunExpired {
		return "", nil
	}
	var payload terminalPayload
	if len(stored.Payload) > 0 {
		if err := json.Unmarshal(stored.Payload, &payload); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}
	if len(payload.Error) == 0 || string(payload.Error) == "null" {
		return "", nil
	}
	if !json.Valid(payload.Error) {
		return "", fmt.Errorf("%w: error must be valid json", ErrInvalidPayload)
	}
	return string(payload.Error), nil
}

func readRunState(ctx context.Context, tx pgx.Tx, userID string, runID string) (string, int, error) {
	var status string
	var version int
	if err := tx.QueryRow(ctx, `
		SELECT status, run_version
		FROM agent_runs
		WHERE run_id = $1 AND user_id = $2
	`, runID, userID).Scan(&status, &version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
			return "", 0, fmt.Errorf("%w: run %s not found", ErrInvalidTransition, runID)
		}
		return "", 0, fmt.Errorf("read run state: %w", err)
	}
	return status, version, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
