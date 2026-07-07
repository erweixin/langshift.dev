package outline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Store) GetOutline(ctx context.Context, userID string, outlineID string) (OutlineRecord, error) {
	if s == nil || s.pool == nil {
		return OutlineRecord{}, errMissingStore
	}
	userID = strings.TrimSpace(userID)
	outlineID = strings.TrimSpace(outlineID)
	if userID == "" || outlineID == "" {
		return OutlineRecord{}, fmt.Errorf("%w: user_id and outline_id are required", ErrInvalidRequest)
	}

	var requestJSON []byte
	var outlineJSON []byte
	record := OutlineRecord{}
	if err := s.pool.QueryRow(ctx, `
		SELECT user_id, source_run_id, request, outline, COALESCE(llm_ledger_id, ''), COALESCE(source_attempt_key, ''), created_at, updated_at
		FROM learning_outlines
		WHERE user_id = $1 AND outline_id = $2
	`, userID, outlineID).Scan(
		&record.UserID,
		&record.RunID,
		&requestJSON,
		&outlineJSON,
		&record.LLMLedgerID,
		&record.SourceAttemptKey,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OutlineRecord{}, ErrNotFound
		}
		return OutlineRecord{}, fmt.Errorf("read learning outline: %w", err)
	}
	if err := json.Unmarshal(requestJSON, &record.Input); err != nil {
		return OutlineRecord{}, fmt.Errorf("parse outline request: %w", err)
	}
	if err := json.Unmarshal(outlineJSON, &record.Outline); err != nil {
		return OutlineRecord{}, fmt.Errorf("parse learning outline: %w", err)
	}
	return record, nil
}

func (s *Store) GetRun(ctx context.Context, userID string, runID string) (RunResult, error) {
	if s == nil || s.pool == nil {
		return RunResult{}, errMissingStore
	}
	userID = strings.TrimSpace(userID)
	runID = strings.TrimSpace(runID)
	if userID == "" || runID == "" {
		return RunResult{}, fmt.Errorf("%w: user_id and run_id are required", ErrInvalidRequest)
	}

	var errorJSON []byte
	result := RunResult{}
	if err := s.pool.QueryRow(ctx, `
		SELECT
			r.run_id,
			r.status,
			COALESCE((
				SELECT e.payload->>'outline_id'
				FROM agent_events e
				WHERE e.user_id = r.user_id
					AND e.run_id = r.run_id
					AND e.type = 'RunSucceeded'
				ORDER BY e.seq DESC
				LIMIT 1
			), '') AS outline_id,
			COALESCE(r.error, 'null'::jsonb)
		FROM agent_runs r
		WHERE r.user_id = $1 AND r.run_id = $2
	`, userID, runID).Scan(
		&result.RunID,
		&result.Status,
		&result.OutlineID,
		&errorJSON,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RunResult{}, ErrNotFound
		}
		return RunResult{}, fmt.Errorf("read outline generation run: %w", err)
	}
	if len(errorJSON) > 0 && string(errorJSON) != "null" {
		result.Error = json.RawMessage(errorJSON)
	}
	return result, nil
}
