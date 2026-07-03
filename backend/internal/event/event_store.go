package event

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Service) appendEvents(ctx context.Context, tx pgx.Tx, request AppendRequest, commandID string) ([]StoredEvent, error) {
	if len(request.Events) == 0 {
		return nil, nil
	}

	nextSeq, err := lockNextSeq(ctx, tx, request.UserID)
	if err != nil {
		return nil, err
	}

	events := make([]StoredEvent, 0, len(request.Events))
	for i, draft := range request.Events {
		payload, err := normalizeJSON(draft.Payload)
		if err != nil {
			return nil, fmt.Errorf("%w: event payload for %s: %v", ErrInvalidRequest, draft.Type, err)
		}
		eventID, err := s.ids.NewID()
		if err != nil {
			return nil, fmt.Errorf("generate event id: %w", err)
		}
		seq := nextSeq + int64(i)
		event := StoredEvent{
			EventID:        eventID,
			Seq:            seq,
			Type:           draft.Type,
			SchemaVersion:  draft.SchemaVersion,
			UserID:         request.UserID,
			MissionID:      draft.MissionID,
			TaskID:         draft.TaskID,
			RunID:          draft.RunID,
			ConversationID: draft.ConversationID,
			CommandID:      commandID,
			CausationID:    draft.CausationID,
			CorrelationID:  draft.CorrelationID,
			Payload:        payload,
		}

		if err := tx.QueryRow(ctx, `
			INSERT INTO events (
				event_id, seq, type, schema_version, user_id, mission_id,
				task_id, run_id, conversation_id, command_id, causation_id,
				correlation_id, payload
			)
			VALUES (
				$1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''),
				nullif($8, ''), nullif($9, ''), nullif($10, ''),
				nullif($11, ''), nullif($12, ''), $13::jsonb
			)
			RETURNING created_at
		`, event.EventID, event.Seq, event.Type, event.SchemaVersion, event.UserID,
			event.MissionID, event.TaskID, event.RunID, event.ConversationID,
			event.CommandID, event.CausationID, event.CorrelationID, string(event.Payload),
		).Scan(&event.CreatedAt); err != nil {
			return nil, fmt.Errorf("insert event %s: %w", draft.Type, err)
		}

		if err := s.dispatcher.Apply(ctx, tx, event); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProjectionDispatch, err)
		}
		events = append(events, event)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE event_cursors
		SET next_seq = $2, updated_at = now()
		WHERE user_id = $1
	`, request.UserID, nextSeq+int64(len(request.Events))); err != nil {
		return nil, fmt.Errorf("advance event cursor: %w", err)
	}

	return events, nil
}
