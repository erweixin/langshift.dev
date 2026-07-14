package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

type expiredPreviewClaim struct {
	inboxID, oldAttemptID, newAttemptID string
	oldFence, newFence                  uint64
	oldDigest, newDigest                []byte
	oldExpiry, newExpiry, now           time.Time
	newToken                            string
}

func (store RunStore) reclaimExpiredToolPreview(ctx context.Context, tx pgx.Tx, command ClaimToolPreviewCommand, claim expiredPreviewClaim) (PreviewClaim, error) {
	if claim.newFence != claim.oldFence+1 || claim.oldExpiry.After(claim.now) || !claim.newExpiry.After(claim.now) {
		return PreviewClaim{}, ErrClaimConflict
	}
	var result PreviewClaim
	var groupKind, effectStatus string
	var lockedFence uint64
	err := tx.QueryRow(ctx, `SELECT t.user_id::text,t.run_id::text,m.group_id::text,g.group_kind,t.tool_call_version,t.current_fence,t.effect_class,t.effect_key,e.id::text,e.status FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id JOIN agent.parallel_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE t.tenant_id=$1 AND t.id=$2 AND t.status='preparing_approval' AND t.active_command_id=$3 AND t.active_attempt_id=$4 AND t.current_fence=$5 AND t.lease_token_hash=$6 AND t.lease_expires_at=$7 AND t.lease_expires_at<=$8 FOR UPDATE OF t`, command.Command.TenantID, command.Command.AggregateID, command.Command.CommandID, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry, claim.now).Scan(&result.UserID, &result.RunID, &result.GroupID, &groupKind, &result.ToolCallVersion, &lockedFence, &result.EffectClass, &result.EffectKey, &result.EffectID, &effectStatus)
	if err != nil || lockedFence+1 != claim.newFence || groupKind != "approval_preview" || effectStatus != "prepared" || result.EffectID == "" || result.EffectClass == "read_only" {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	var runStatus string
	var cancelRequested *time.Time
	var dueAt time.Time
	if err = tx.QueryRow(ctx, `SELECT status,cancel_requested_at,due_at FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.Command.TenantID, result.RunID).Scan(&runStatus, &cancelRequested, &dueAt); err != nil || runStatus != string(statemachine.RunWaitingTool) || cancelRequested != nil || !dueAt.After(claim.now) {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	var jobID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM agent.jobs WHERE tenant_id=$1 AND command_id=$2 AND status='running' FOR UPDATE`, command.Command.TenantID, command.Command.CommandID).Scan(&jobID); err != nil {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	var oldAttemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at<=$8 FOR UPDATE`, claim.oldAttemptID, command.Command.TenantID, jobID, command.Command.CommandID, claim.oldFence, claim.oldDigest, claim.oldExpiry, claim.now).Scan(&oldAttemptVersion)
	if err != nil {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='expired',finished_at=$2,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND status='running' AND fence=$6 AND lease_token_hash=$7 AND lease_expires_at=$8`, oldAttemptVersion+1, claim.now, claim.oldAttemptID, command.Command.TenantID, oldAttemptVersion, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return PreviewClaim{}, ErrClaimConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET owner_attempt_id=$1,fence=$2,lease_token_hash=$3,lease_expires_at=$4,updated_at=$5 WHERE id=$6 AND tenant_id=$7 AND store_epoch=$8 AND consumer_name=$9 AND command_id=$10 AND request_hash=$11 AND status='running' AND owner_attempt_id=$12 AND fence=$13 AND lease_token_hash=$14 AND lease_expires_at=$15 AND lease_expires_at<=$5`, claim.newAttemptID, claim.newFence, claim.newDigest, claim.newExpiry, claim.now, claim.inboxID, command.Command.TenantID, command.Command.StoreEpoch, command.ConsumerName, command.Command.CommandID, command.Command.PayloadHash, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return PreviewClaim{}, ErrClaimConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET active_attempt_id=$1,current_fence=$2,lease_token_hash=$3,lease_expires_at=$4,updated_at=$5 WHERE tenant_id=$6 AND id=$7 AND status='preparing_approval' AND tool_call_version=$8 AND active_command_id=$9 AND active_attempt_id=$10 AND current_fence=$11 AND lease_token_hash=$12 AND lease_expires_at=$13 AND lease_expires_at<=$5`, claim.newAttemptID, claim.newFence, claim.newDigest, claim.newExpiry, claim.now, command.Command.TenantID, command.Command.AggregateID, result.ToolCallVersion, command.Command.CommandID, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return PreviewClaim{}, ErrClaimConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'running',$9,$9,$9)`, claim.newAttemptID, command.Command.TenantID, jobID, command.Command.CommandID, claim.newFence, claim.newDigest, claim.newExpiry, command.WorkerID, claim.now); err != nil {
		return PreviewClaim{}, err
	}
	completedIDs, err := store.completionEventIdentifiers(claim.oldAttemptID, statemachine.RunExpired)
	if err != nil {
		return PreviewClaim{}, err
	}
	startedIDs, err := store.previewClaimEventIDs(claim.newAttemptID)
	if err != nil {
		return PreviewClaim{}, err
	}
	oldStartedIDs, err := store.previewClaimEventIDs(claim.oldAttemptID)
	if err != nil {
		return PreviewClaim{}, err
	}
	oldCausation := oldStartedIDs.attemptEvent
	completed := eventpostgres.Input{Event: eventpostgres.Event{ID: completedIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: result.UserID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.oldAttemptID, AggregateVersion: oldAttemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: claim.now, Actor: command.Actor, CausationID: &oldCausation, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completedIDs.attemptOutbox, CommandID: completedIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, completed); err != nil {
		return PreviewClaim{}, err
	}
	newCausation := completedIDs.attemptEvent
	started := eventpostgres.Input{Event: eventpostgres.Event{ID: startedIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: result.UserID, EventType: "JobAttemptStarted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.newAttemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: claim.now, Actor: command.Actor, CausationID: &newCausation, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: startedIDs.attemptOutbox, CommandID: startedIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, started); err != nil {
		return PreviewClaim{}, err
	}
	result.ToolCallID, result.TenantID, result.StoreEpoch = command.Command.AggregateID, command.Command.TenantID, command.Command.StoreEpoch
	result.CommandID, result.ConsumerName, result.RequestHash = command.Command.CommandID, command.ConsumerName, command.Command.PayloadHash
	result.JobID, result.InboxID, result.AttemptID = jobID, claim.inboxID, claim.newAttemptID
	result.Fence, result.LeaseToken, result.LeaseExpiresAt = claim.newFence, claim.newToken, claim.newExpiry
	return result, nil
}
