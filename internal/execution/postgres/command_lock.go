package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// lockCommandDelivery serializes inbox owner installation with redelivery
// reconciliation, including the otherwise invisible uncommitted INSERT case.
// Command IDs are globally unique in the outbox contract; hash collisions only
// reduce concurrency and cannot weaken correctness.
func lockCommandDelivery(ctx context.Context, tx pgx.Tx, commandID string) error {
	if tx == nil || commandID == "" {
		return ErrConfiguration
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, commandID)
	return err
}
