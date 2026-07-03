package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func validateJobFence(ctx context.Context, tx pgx.Tx, fence JobFence) (string, error) {
	var commandID string
	var status string
	var leaseToken sql.NullString
	var leaseUntil sql.NullTime
	if err := tx.QueryRow(ctx, `
		SELECT command_id, status, lease_token, lease_until
		FROM jobs
		WHERE job_id = $1
		FOR UPDATE
	`, fence.JobID).Scan(&commandID, &status, &leaseToken, &leaseUntil); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrJobFenceInvalid
		}
		return "", fmt.Errorf("read job fence: %w", err)
	}
	if status != "leased" || !leaseToken.Valid || leaseToken.String != fence.LeaseToken || !leaseUntil.Valid || !leaseUntil.Time.After(time.Now()) {
		return "", ErrJobFenceInvalid
	}
	return commandID, nil
}

func markJobDone(ctx context.Context, tx pgx.Tx, fence JobFence) error {
	tag, err := tx.Exec(ctx, `
		UPDATE jobs
		SET status = 'done',
			lease_until = NULL,
			lease_token = NULL,
			leased_by = NULL,
			heartbeat_at = NULL,
			last_error = NULL,
			updated_at = now()
		WHERE job_id = $1
			AND status = 'leased'
			AND lease_token = $2
			AND lease_until > now()
	`, fence.JobID, fence.LeaseToken)
	if err != nil {
		return fmt.Errorf("mark job done: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrJobFenceInvalid
	}
	return nil
}
