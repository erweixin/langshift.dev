package event

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) enqueueCommands(ctx context.Context, tx pgx.Tx, request AppendRequest) ([]StoredCommand, error) {
	commands := make([]StoredCommand, 0, len(request.Commands))
	for _, draft := range request.Commands {
		payload, err := normalizeJSON(draft.Payload)
		if err != nil {
			return nil, fmt.Errorf("%w: command payload for %s: %v", ErrInvalidRequest, draft.Kind, err)
		}
		jobID, err := s.ids.NewID()
		if err != nil {
			return nil, fmt.Errorf("generate job id: %w", err)
		}
		commandID, err := s.ids.NewID()
		if err != nil {
			return nil, fmt.Errorf("generate child command id: %w", err)
		}
		subjectUserID := draft.SubjectUserID
		if subjectUserID == "" {
			subjectUserID = request.UserID
		}

		var dueAt time.Time
		if draft.DueAt != nil {
			err = tx.QueryRow(ctx, `
				INSERT INTO agent_jobs (
					job_id, command_id, kind, subject_user_id, payload, due_at
				)
				VALUES ($1, $2, $3, nullif($4, ''), $5::jsonb, $6)
				RETURNING due_at
			`, jobID, commandID, draft.Kind, subjectUserID, string(payload), *draft.DueAt).Scan(&dueAt)
		} else {
			err = tx.QueryRow(ctx, `
				INSERT INTO agent_jobs (
					job_id, command_id, kind, subject_user_id, payload
				)
				VALUES ($1, $2, $3, nullif($4, ''), $5::jsonb)
				RETURNING due_at
			`, jobID, commandID, draft.Kind, subjectUserID, string(payload)).Scan(&dueAt)
		}
		if err != nil {
			return nil, fmt.Errorf("enqueue command %s: %w", draft.Kind, err)
		}

		commands = append(commands, StoredCommand{
			JobID:         jobID,
			CommandID:     commandID,
			Kind:          draft.Kind,
			SubjectUserID: subjectUserID,
			DueAt:         dueAt,
			Payload:       payload,
		})
	}
	return commands, nil
}
