package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/behavior"
)

var ErrRunNotFound = errors.New("run was not found in the authorized tenant scope")

type RunSnapshot struct {
	ID        string
	Version   uint64
	Status    string
	UpdatedAt time.Time
}

// ResolveBehavior snapshots the current production binding for payload
// preparation. AcceptInTx re-resolves and fences on the same binding, so a
// concurrent channel promotion cannot produce an event/projection mismatch.
func (store RunStore) ResolveBehavior(ctx context.Context, tenantID string, profile behavior.Profile, environment string) (behavior.ChannelBinding, error) {
	if store.Pool == nil || store.Behavior == nil || tenantID == "" || !profile.Valid() || environment != "staging" && environment != "production" {
		return behavior.ChannelBinding{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return behavior.ChannelBinding{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return behavior.ChannelBinding{}, err
	}
	binding, err := store.Behavior.ResolveCurrent(ctx, tx, tenantID, profile, environment)
	if err != nil {
		return behavior.ChannelBinding{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return behavior.ChannelBinding{}, err
	}
	return binding, nil
}

// GetRun returns only the public projection fields and scopes the lookup by
// both tenant and owner. It intentionally does not expose payload manifests,
// lease material, command IDs, or internal scheduling state.
func (store RunStore) GetRun(ctx context.Context, tenantID, userID, runID string) (RunSnapshot, error) {
	if store.Pool == nil || tenantID == "" || userID == "" || runID == "" {
		return RunSnapshot{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return RunSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return RunSnapshot{}, err
	}
	var result RunSnapshot
	err = tx.QueryRow(ctx, `SELECT id::text,run_version,status,updated_at FROM agent.runs WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, runID).Scan(&result.ID, &result.Version, &result.Status, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunSnapshot{}, ErrRunNotFound
	}
	if err != nil {
		return RunSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RunSnapshot{}, err
	}
	result.UpdatedAt = result.UpdatedAt.UTC()
	return result, nil
}
