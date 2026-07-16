package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

type expiredRunClaim struct {
	inboxID, oldAttemptID, newAttemptID string
	oldFence, newFence                  uint64
	oldDigest, newDigest                []byte
	oldExpiry, newExpiry, now           time.Time
	newToken                            string
}

// reclaimExpiredRun executes after the inbox row has been locked. It replaces
// an expired execution right without changing the Run's business state or
// version, records the old attempt as expired, and starts a fresh attempt.
func (store RunStore) reclaimExpiredRun(ctx context.Context, tx pgx.Tx, command ClaimRunCommand, claim expiredRunClaim) (RunClaim, error) {
	if claim.newFence != claim.oldFence+1 || claim.oldExpiry.After(claim.now) || !claim.newExpiry.After(claim.now) {
		return RunClaim{}, ErrClaimConflict
	}
	var userID string
	var runVersion, lockedFence uint64
	var dueAt, createdAt time.Time
	err := tx.QueryRow(ctx, `SELECT user_id::text,run_version,current_fence,due_at,created_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND status='executing' AND active_command_id=$3 AND active_attempt_id=$4 AND current_fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at<=$8 AND cancel_requested_at IS NULL AND due_at>$8 FOR UPDATE`, command.Command.AggregateID, command.Command.TenantID, command.Command.CommandID, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry, claim.now).Scan(&userID, &runVersion, &lockedFence, &dueAt, &createdAt)
	if err != nil || lockedFence+1 != claim.newFence {
		return RunClaim{}, ErrRunNotClaimable
	}
	var jobID, queueClass string
	if err = tx.QueryRow(ctx, `SELECT id::text,queue_class FROM agent.jobs WHERE tenant_id=$1 AND command_id=$2 AND status='running' FOR UPDATE`, command.Command.TenantID, command.Command.CommandID).Scan(&jobID, &queueClass); err != nil {
		return RunClaim{}, ErrRunNotClaimable
	}
	var oldAttemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at<=$8 FOR UPDATE`, claim.oldAttemptID, command.Command.TenantID, jobID, command.Command.CommandID, claim.oldFence, claim.oldDigest, claim.oldExpiry, claim.now).Scan(&oldAttemptVersion)
	if err != nil {
		return RunClaim{}, ErrRunNotClaimable
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='expired',finished_at=$2,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND status='running' AND fence=$6 AND lease_token_hash=$7 AND lease_expires_at=$8`, oldAttemptVersion+1, claim.now, claim.oldAttemptID, command.Command.TenantID, oldAttemptVersion, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return RunClaim{}, ErrClaimConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET owner_attempt_id=$1,fence=$2,lease_token_hash=$3,lease_expires_at=$4,updated_at=$5 WHERE id=$6 AND tenant_id=$7 AND store_epoch=$8 AND consumer_name=$9 AND command_id=$10 AND request_hash=$11 AND status='running' AND owner_attempt_id=$12 AND fence=$13 AND lease_token_hash=$14 AND lease_expires_at=$15 AND lease_expires_at<=$5`, claim.newAttemptID, claim.newFence, claim.newDigest, claim.newExpiry, claim.now, claim.inboxID, command.Command.TenantID, command.Command.StoreEpoch, command.ConsumerName, command.Command.CommandID, command.Command.PayloadHash, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return RunClaim{}, ErrClaimConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET active_attempt_id=$1,current_fence=$2,lease_token_hash=$3,lease_expires_at=$4,updated_at=$5 WHERE id=$6 AND tenant_id=$7 AND status='executing' AND run_version=$8 AND active_command_id=$9 AND active_attempt_id=$10 AND current_fence=$11 AND lease_token_hash=$12 AND lease_expires_at=$13 AND lease_expires_at<=$5`, claim.newAttemptID, claim.newFence, claim.newDigest, claim.newExpiry, claim.now, command.Command.AggregateID, command.Command.TenantID, runVersion, command.Command.CommandID, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return RunClaim{}, ErrClaimConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.job_attempts (id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'running',$9,$9,$9)`, claim.newAttemptID, command.Command.TenantID, jobID, command.Command.CommandID, claim.newFence, claim.newDigest, claim.newExpiry, command.WorkerID, claim.now); err != nil {
		return RunClaim{}, err
	}
	completedIDs, err := store.completionEventIdentifiers(claim.oldAttemptID, statemachine.RunExpired)
	if err != nil {
		return RunClaim{}, err
	}
	startedIDs, err := store.claimEventIdentifiers(claim.newAttemptID)
	if err != nil {
		return RunClaim{}, err
	}
	oldStartedIDs, err := store.claimEventIdentifiers(claim.oldAttemptID)
	if err != nil {
		return RunClaim{}, err
	}
	oldCausationID := oldStartedIDs.attemptEvent
	completed := eventpostgres.Input{Event: eventpostgres.Event{ID: completedIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.oldAttemptID, AggregateVersion: oldAttemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: claim.now, Actor: command.Actor, CausationID: &oldCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completedIDs.attemptOutbox, CommandID: completedIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, completed); err != nil {
		return RunClaim{}, err
	}
	newCausationID := completedIDs.attemptEvent
	started := eventpostgres.Input{Event: eventpostgres.Event{ID: startedIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: userID, EventType: "JobAttemptStarted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.newAttemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: claim.now, Actor: command.Actor, CausationID: &newCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: startedIDs.attemptOutbox, CommandID: startedIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, started); err != nil {
		return RunClaim{}, err
	}
	return RunClaim{RunID: command.Command.AggregateID, TenantID: command.Command.TenantID, UserID: userID, StoreEpoch: command.Command.StoreEpoch, RunVersion: runVersion, CommandID: command.Command.CommandID, ConsumerName: command.ConsumerName, RequestHash: command.Command.PayloadHash, JobID: jobID, InboxID: claim.inboxID, AttemptID: claim.newAttemptID, Fence: claim.newFence, LeaseToken: claim.newToken, LeaseExpiresAt: claim.newExpiry, DueAt: dueAt, CreatedAt: createdAt, QueueClass: queueClass}, nil
}
