package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

type expiredReconciliationClaim struct {
	inboxID, oldAttemptID, newAttemptID string
	oldFence, newFence                  uint64
	oldDigest, newDigest                []byte
	oldExpiry, newExpiry, now           time.Time
	newToken                            string
}

func (store RunStore) reclaimExpiredReconciliation(ctx context.Context, tx pgx.Tx, command ClaimReconciliationCommand, expired expiredReconciliationClaim) (ReconciliationClaim, error) {
	if expired.newFence != expired.oldFence+1 || expired.oldExpiry.After(expired.now) || !expired.newExpiry.After(expired.now) {
		return ReconciliationClaim{}, ErrClaimConflict
	}
	claim, err := store.lockReconciliationTarget(ctx, tx, command.Command.TenantID, command.Command.AggregateID, expired.now)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	var jobID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM agent.jobs WHERE tenant_id=$1 AND command_id=$2 AND status='running' FOR UPDATE`, command.Command.TenantID, command.Command.CommandID).Scan(&jobID); err != nil {
		return ReconciliationClaim{}, ErrReconciliationNotClaimable
	}
	var oldAttemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at<=$8 FOR UPDATE`, expired.oldAttemptID, command.Command.TenantID, jobID, command.Command.CommandID, expired.oldFence, expired.oldDigest, expired.oldExpiry, expired.now).Scan(&oldAttemptVersion)
	if err != nil {
		return ReconciliationClaim{}, ErrReconciliationNotClaimable
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='expired',finished_at=$2,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND status='running' AND fence=$6 AND lease_token_hash=$7 AND lease_expires_at=$8`, oldAttemptVersion+1, expired.now, expired.oldAttemptID, command.Command.TenantID, oldAttemptVersion, expired.oldFence, expired.oldDigest, expired.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return ReconciliationClaim{}, ErrClaimConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET owner_attempt_id=$1,fence=$2,lease_token_hash=$3,lease_expires_at=$4,updated_at=$5 WHERE id=$6 AND tenant_id=$7 AND store_epoch=$8 AND consumer_name=$9 AND command_id=$10 AND request_hash=$11 AND status='running' AND owner_attempt_id=$12 AND fence=$13 AND lease_token_hash=$14 AND lease_expires_at=$15 AND lease_expires_at<=$5`, expired.newAttemptID, expired.newFence, expired.newDigest, expired.newExpiry, expired.now, expired.inboxID, command.Command.TenantID, command.Command.StoreEpoch, command.ConsumerName, command.Command.CommandID, command.Command.PayloadHash, expired.oldAttemptID, expired.oldFence, expired.oldDigest, expired.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return ReconciliationClaim{}, ErrClaimConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'running',$9,$9,$9)`, expired.newAttemptID, command.Command.TenantID, jobID, command.Command.CommandID, expired.newFence, expired.newDigest, expired.newExpiry, command.WorkerID, expired.now); err != nil {
		return ReconciliationClaim{}, err
	}
	completedIDs, err := store.completionEventIdentifiers(expired.oldAttemptID, statemachine.RunExpired)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	oldStartedIDs, err := store.reconciliationClaimEventIdentifiers(expired.oldAttemptID)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	newStartedIDs, err := store.reconciliationClaimEventIdentifiers(expired.newAttemptID)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	oldCausationID := oldStartedIDs.event
	completed := eventpostgres.Input{Event: eventpostgres.Event{ID: completedIDs.attemptEvent, TenantID: claim.TenantID, UserID: claim.UserID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: expired.oldAttemptID, AggregateVersion: oldAttemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: expired.now, Actor: command.Actor, CausationID: &oldCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completedIDs.attemptOutbox, CommandID: completedIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, completed); err != nil {
		return ReconciliationClaim{}, err
	}
	newCausationID := completedIDs.attemptEvent
	started := eventpostgres.Input{Event: eventpostgres.Event{ID: newStartedIDs.event, TenantID: claim.TenantID, UserID: claim.UserID, EventType: "JobAttemptStarted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: expired.newAttemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: expired.now, Actor: command.Actor, CausationID: &newCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: newStartedIDs.outbox, CommandID: newStartedIDs.publish, CommandType: "events.publish", PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, started); err != nil {
		return ReconciliationClaim{}, err
	}
	claim.StoreEpoch, claim.CommandID, claim.ConsumerName, claim.RequestHash, claim.JobID, claim.InboxID = command.Command.StoreEpoch, command.Command.CommandID, command.ConsumerName, command.Command.PayloadHash, jobID, expired.inboxID
	claim.AttemptID, claim.Fence, claim.LeaseToken, claim.LeaseExpiresAt = expired.newAttemptID, expired.newFence, expired.newToken, expired.newExpiry
	return claim, nil
}
