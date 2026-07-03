package event

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func advanceRunVersion(ctx context.Context, tx pgx.Tx, userID string, aggregate RunAggregate) (int, error) {
	var version int
	if err := tx.QueryRow(ctx, `
		UPDATE runs
		SET run_version = run_version + 1,
			updated_at = now()
		WHERE run_id = $1
			AND user_id = $2
			AND run_version = $3
		RETURNING run_version
	`, aggregate.RunID, userID, aggregate.ExpectedVersion).Scan(&version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrRunVersionConflict
		}
		return 0, fmt.Errorf("advance run version: %w", err)
	}
	return version, nil
}
