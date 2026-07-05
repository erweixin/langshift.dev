package outline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/contracts"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) SaveOutline(ctx context.Context, request SaveOutlineRequest) (OutlineRecord, error) {
	if s == nil || s.pool == nil {
		return OutlineRecord{}, errMissingStore
	}
	if strings.TrimSpace(request.UserID) == "" || strings.TrimSpace(request.RunID) == "" {
		return OutlineRecord{}, fmt.Errorf("%w: user_id and run_id are required", ErrInvalidRequest)
	}
	input, err := normalizeInput(request.Input)
	if err != nil {
		return OutlineRecord{}, err
	}
	outline := request.Outline
	if err := validateOutline(outline); err != nil {
		return OutlineRecord{}, err
	}

	requestJSON, err := json.Marshal(input)
	if err != nil {
		return OutlineRecord{}, fmt.Errorf("marshal outline request: %w", err)
	}
	outlineJSON, err := json.Marshal(outline)
	if err != nil {
		return OutlineRecord{}, fmt.Errorf("marshal learning outline: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OutlineRecord{}, fmt.Errorf("begin save outline: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if _, err := tx.Exec(ctx, `
		INSERT INTO learning_outlines (
			outline_id,
			user_id,
			source_run_id,
			request,
			outline,
			llm_ledger_id,
			source_attempt_key
		)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, nullif($6, ''), nullif($7, ''))
		ON CONFLICT (outline_id) DO NOTHING
	`, outline.OutlineID, request.UserID, request.RunID, string(requestJSON), string(outlineJSON), request.LLMLedgerID, request.SourceAttemptKey); err != nil {
		return OutlineRecord{}, fmt.Errorf("insert learning outline: %w", err)
	}

	for _, task := range outline.Tasks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning_tasks (
				task_id,
				outline_id,
				user_id,
				day_index,
				task_template_id,
				target_stack,
				level_band,
				title,
				judge,
				minutes
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (task_id) DO NOTHING
		`, taskID(outline.OutlineID, task.DayIndex), outline.OutlineID, request.UserID, task.DayIndex, task.TaskTemplateID, task.TargetStack, task.LevelBand, task.Title, task.Judge, task.Minutes); err != nil {
			return OutlineRecord{}, fmt.Errorf("insert learning task day %d: %w", task.DayIndex, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return OutlineRecord{}, fmt.Errorf("commit save outline: %w", err)
	}
	committed = true
	return s.GetOutline(ctx, request.UserID, outline.OutlineID)
}

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
				FROM events e
				WHERE e.user_id = r.user_id
					AND e.run_id = r.run_id
					AND e.type = 'RunSucceeded'
				ORDER BY e.seq DESC
				LIMIT 1
			), '') AS outline_id,
			COALESCE(r.error, 'null'::jsonb)
		FROM runs r
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

func PublicOutline(record OutlineRecord) contracts.LearningOutline {
	return record.Outline
}

func taskID(outlineID string, dayIndex int) string {
	return fmt.Sprintf("%s_task_%02d", outlineID, dayIndex)
}
