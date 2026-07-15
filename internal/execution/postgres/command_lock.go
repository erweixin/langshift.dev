package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
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

func lockScheduledCommandDelivery(ctx context.Context, tx pgx.Tx, command eventpostgres.DeliveredCommand, required bool) error {
	if err := lockCommandDelivery(ctx, tx, command.CommandID); err != nil {
		return err
	}
	if command.QueueGeneration == 0 && command.DispatchVersion == 0 && !required {
		return nil
	}
	if command.QueueGeneration < 1 || command.DispatchVersion < 1 {
		return ErrClaimConflict
	}
	var matches bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.jobs WHERE tenant_id=$1 AND command_id=$2 AND queue_generation=$3 AND dispatch_version=$4 AND status IN ('pending','running') FOR UPDATE)`, command.TenantID, command.CommandID, command.QueueGeneration, command.DispatchVersion).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrClaimConflict
	}
	return nil
}
