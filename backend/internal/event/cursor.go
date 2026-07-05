package event

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func lockNextSeq(ctx context.Context, tx pgx.Tx, userID string) (int64, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_event_cursors (user_id, next_seq)
		VALUES ($1, 1)
		ON CONFLICT (user_id) DO NOTHING
	`, userID); err != nil {
		return 0, fmt.Errorf("ensure event cursor: %w", err)
	}

	var nextSeq int64
	if err := tx.QueryRow(ctx, `
		SELECT next_seq
		FROM agent_event_cursors
		WHERE user_id = $1
		FOR UPDATE
	`, userID).Scan(&nextSeq); err != nil {
		return 0, fmt.Errorf("lock event cursor: %w", err)
	}
	return nextSeq, nil
}
