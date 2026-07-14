package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// HeartbeatReconciliation renews only the provider-lookup inbox and attempt.
func (store RunStore) HeartbeatReconciliation(ctx context.Context, claim ReconciliationClaim) (ReconciliationClaim, error) {
	if !store.validClaim() || !validReconciliationClaim(claim) {
		return ReconciliationClaim{}, ErrConfiguration
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return ReconciliationClaim{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return ReconciliationClaim{}, ErrExecutionRightConflict
	}
	now := store.claimNow()
	expiresAt := now.Add(store.LeaseTTL).UTC().Truncate(time.Microsecond)
	if !expiresAt.After(claim.LeaseExpiresAt) {
		return ReconciliationClaim{}, ErrExecutionRightConflict
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ReconciliationClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return ReconciliationClaim{}, err
	}
	if err = lockReconciliationInbox(ctx, tx, claim, digest[:], now); err != nil {
		return ReconciliationClaim{}, err
	}
	var lockedEffectVersion, lockedToolVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.tool_effects WHERE id=$1 AND tenant_id=$2 AND tool_call_id=$3 AND run_id=$4 AND status='outcome_unknown' AND version=$5 FOR UPDATE`, claim.EffectID, claim.TenantID, claim.ToolCallID, claim.RunID, claim.EffectVersion).Scan(&lockedEffectVersion)
	if err != nil {
		return ReconciliationClaim{}, ErrExecutionRightConflict
	}
	err = tx.QueryRow(ctx, `SELECT tool_call_version FROM agent.tool_calls WHERE id=$1 AND tenant_id=$2 AND run_id=$3 AND status='outcome_unknown' AND tool_call_version=$4 FOR UPDATE`, claim.ToolCallID, claim.TenantID, claim.RunID, claim.ToolCallVersion).Scan(&lockedToolVersion)
	if err != nil {
		return ReconciliationClaim{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return ReconciliationClaim{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET lease_expires_at=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND status='running' AND owner_attempt_id=$5 AND fence=$6 AND lease_token_hash=$7 AND lease_expires_at=$8`, expiresAt, now, claim.InboxID, claim.TenantID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return ReconciliationClaim{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,lease_expires_at=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND status='running' AND version=$6 AND command_id=$7 AND fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10`, attemptVersion+1, expiresAt, now, claim.AttemptID, claim.TenantID, attemptVersion, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return ReconciliationClaim{}, ErrExecutionRightConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return ReconciliationClaim{}, err
	}
	claim.LeaseExpiresAt = expiresAt
	return claim, nil
}

func lockReconciliationInbox(ctx context.Context, tx pgx.Tx, claim ReconciliationClaim, digest []byte, now time.Time) error {
	var locked string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent.inbox WHERE id=$1 AND tenant_id=$2 AND store_epoch=$3 AND consumer_name=$4 AND command_id=$5 AND request_hash=$6 AND status='running' AND owner_attempt_id=$7 AND fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$11 FOR UPDATE`, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest, claim.LeaseExpiresAt, now).Scan(&locked)
	if err != nil || locked != claim.InboxID {
		return ErrExecutionRightConflict
	}
	return nil
}
