package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Store) GetArtifact(ctx context.Context, contentKey string) (ArtifactRecord, error) {
	if s == nil || s.pool == nil {
		return ArtifactRecord{}, errMissingStore
	}
	contentKey = strings.TrimSpace(contentKey)
	if contentKey == "" {
		return ArtifactRecord{}, fmt.Errorf("%w: content_key is required", ErrInvalidRequest)
	}

	var encoded []byte
	record := ArtifactRecord{}
	if err := s.pool.QueryRow(ctx, `
		SELECT artifact, artifact_hash, review_status, validation_attempts, created_at, updated_at
		FROM content_artifacts
		WHERE content_key = $1
	`, contentKey).Scan(
		&encoded,
		&record.ArtifactHash,
		&record.ReviewStatus,
		&record.ValidationAttempts,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ArtifactRecord{}, ErrNotFound
		}
		return ArtifactRecord{}, fmt.Errorf("read content artifact: %w", err)
	}
	if err := json.Unmarshal(encoded, &record.Artifact); err != nil {
		return ArtifactRecord{}, fmt.Errorf("parse content artifact: %w", err)
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
				SELECT e.payload->>'content_key'
				FROM agent_events e
				WHERE e.user_id = r.user_id
					AND e.run_id = r.run_id
					AND e.type = 'RunSucceeded'
				ORDER BY e.seq DESC
				LIMIT 1
			), '') AS content_key,
			COALESCE(r.error, 'null'::jsonb)
		FROM agent_runs r
		WHERE r.user_id = $1 AND r.run_id = $2
	`, userID, runID).Scan(
		&result.RunID,
		&result.Status,
		&result.ContentKey,
		&errorJSON,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RunResult{}, ErrNotFound
		}
		return RunResult{}, fmt.Errorf("read content generation run: %w", err)
	}
	if len(errorJSON) > 0 && string(errorJSON) != "null" {
		result.Error = json.RawMessage(errorJSON)
	}
	return result, nil
}
