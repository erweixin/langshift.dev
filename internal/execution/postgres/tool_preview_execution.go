package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// HeartbeatToolPreview renews the exact inbox, ToolCall, and Attempt right for
// an isolated approval preview. It cannot renew a normal execution lease.
func (store RunStore) HeartbeatToolPreview(ctx context.Context, claim PreviewClaim) (PreviewClaim, error) {
	if !store.validClaim() || !validPreviewClaim(claim) {
		return PreviewClaim{}, ErrConfiguration
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return PreviewClaim{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return PreviewClaim{}, ErrExecutionRightConflict
	}
	now := store.claimNow()
	expiresAt := now.Add(store.LeaseTTL).UTC().Truncate(time.Microsecond)
	if !expiresAt.After(claim.LeaseExpiresAt) {
		return PreviewClaim{}, ErrExecutionRightConflict
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PreviewClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return PreviewClaim{}, err
	}
	if err = lockPreviewInbox(ctx, tx, claim, digest[:], now); err != nil {
		return PreviewClaim{}, err
	}
	var locked string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agent.tool_calls WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND run_id=$4 AND status='preparing_approval' AND tool_call_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$11 FOR UPDATE`, claim.ToolCallID, claim.TenantID, claim.UserID, claim.RunID, claim.ToolCallVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&locked)
	if err != nil || locked != claim.ToolCallID {
		return PreviewClaim{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return PreviewClaim{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET lease_expires_at=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND status='running' AND owner_attempt_id=$5 AND fence=$6 AND lease_token_hash=$7 AND lease_expires_at=$8`, expiresAt, now, claim.InboxID, claim.TenantID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return PreviewClaim{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET lease_expires_at=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND status='preparing_approval' AND tool_call_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10`, expiresAt, now, claim.ToolCallID, claim.TenantID, claim.ToolCallVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return PreviewClaim{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,lease_expires_at=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND status='running' AND version=$6 AND job_id=$7 AND command_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11`, attemptVersion+1, expiresAt, now, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return PreviewClaim{}, ErrExecutionRightConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return PreviewClaim{}, err
	}
	claim.LeaseExpiresAt = expiresAt
	return claim, nil
}

func lockPreviewInbox(ctx context.Context, tx pgx.Tx, claim PreviewClaim, digest []byte, now time.Time) error {
	var locked string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent.inbox WHERE id=$1 AND tenant_id=$2 AND store_epoch=$3 AND consumer_name=$4 AND command_id=$5 AND request_hash=$6 AND status='running' AND owner_attempt_id=$7 AND fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$11 FOR UPDATE`, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest, claim.LeaseExpiresAt, now).Scan(&locked)
	if err != nil || locked != claim.InboxID {
		return ErrExecutionRightConflict
	}
	return nil
}
