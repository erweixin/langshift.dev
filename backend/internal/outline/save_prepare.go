package outline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"lites/backend/internal/contracts"
)

type preparedOutlineSave struct {
	requestJSON []byte
	outlineJSON []byte
	record      OutlineRecord
}

func prepareOutlineSave(request SaveOutlineRequest) (preparedOutlineSave, error) {
	if strings.TrimSpace(request.UserID) == "" || strings.TrimSpace(request.RunID) == "" {
		return preparedOutlineSave{}, fmt.Errorf("%w: user_id and run_id are required", ErrInvalidRequest)
	}
	input, err := normalizeInput(request.Input)
	if err != nil {
		return preparedOutlineSave{}, err
	}
	outline := request.Outline
	if err := validateOutline(outline); err != nil {
		return preparedOutlineSave{}, err
	}

	requestJSON, err := json.Marshal(input)
	if err != nil {
		return preparedOutlineSave{}, fmt.Errorf("marshal outline request: %w", err)
	}
	outlineJSON, err := json.Marshal(outline)
	if err != nil {
		return preparedOutlineSave{}, fmt.Errorf("marshal learning outline: %w", err)
	}

	return preparedOutlineSave{
		requestJSON: requestJSON,
		outlineJSON: outlineJSON,
		record: OutlineRecord{
			UserID:           request.UserID,
			RunID:            request.RunID,
			Input:            input,
			Outline:          outline,
			LLMLedgerID:      request.LLMLedgerID,
			SourceAttemptKey: request.SourceAttemptKey,
		},
	}, nil
}

func saveOutlineTx(ctx context.Context, tx pgx.Tx, prepared preparedOutlineSave) error {
	record := prepared.record
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
	`, record.Outline.OutlineID, record.UserID, record.RunID, string(prepared.requestJSON), string(prepared.outlineJSON), record.LLMLedgerID, record.SourceAttemptKey); err != nil {
		return fmt.Errorf("insert learning outline: %w", err)
	}

	for _, task := range record.Outline.Tasks {
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
		`, taskID(record.Outline.OutlineID, task.DayIndex), record.Outline.OutlineID, record.UserID, task.DayIndex, task.TaskTemplateID, task.TargetStack, task.LevelBand, task.Title, task.Judge, task.Minutes); err != nil {
			return fmt.Errorf("insert learning task day %d: %w", task.DayIndex, err)
		}
	}
	return nil
}

func PublicOutline(record OutlineRecord) contracts.LearningOutline {
	return record.Outline
}

func taskID(outlineID string, dayIndex int) string {
	return fmt.Sprintf("%s_task_%02d", outlineID, dayIndex)
}
