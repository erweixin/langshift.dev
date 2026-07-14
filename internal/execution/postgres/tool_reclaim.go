package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

type expiredToolClaim struct {
	inboxID, oldAttemptID, newAttemptID string
	oldFence, newFence                  uint64
	oldDigest, newDigest                []byte
	oldExpiry, newExpiry, now           time.Time
	newToken                            string
}

func (store RunStore) reclaimExpiredTool(ctx context.Context, tx pgx.Tx, command ClaimToolCommand, claim expiredToolClaim) (ToolClaim, error) {
	if claim.newFence != claim.oldFence+1 || claim.oldExpiry.After(claim.now) || !claim.newExpiry.After(claim.now) {
		return ToolClaim{}, ErrClaimConflict
	}
	var effectID, effectClass, effectStatus, providerRequestID string
	var effectVersion uint64
	effectErr := tx.QueryRow(ctx, `SELECT id::text,effect_class,status,version,COALESCE(provider_request_id,'') FROM agent.tool_effects WHERE tenant_id=$1 AND tool_call_id=$2 FOR UPDATE`, command.Command.TenantID, command.Command.AggregateID).Scan(&effectID, &effectClass, &effectStatus, &effectVersion, &providerRequestID)
	hasEffect := effectErr == nil
	if effectErr != nil && !errors.Is(effectErr, pgx.ErrNoRows) {
		return ToolClaim{}, effectErr
	}
	var userID, runID, groupID, toolEffectClass string
	var toolVersion, lockedFence uint64
	err := tx.QueryRow(ctx, `SELECT t.user_id::text,t.run_id::text,m.group_id::text,t.tool_call_version,t.current_fence,t.effect_class FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id WHERE t.id=$1 AND t.tenant_id=$2 AND t.status='executing' AND t.active_command_id=$3 AND t.active_attempt_id=$4 AND t.current_fence=$5 AND t.lease_token_hash=$6 AND t.lease_expires_at=$7 AND t.lease_expires_at<=$8 FOR UPDATE OF t`, command.Command.AggregateID, command.Command.TenantID, command.Command.CommandID, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry, claim.now).Scan(&userID, &runID, &groupID, &toolVersion, &lockedFence, &toolEffectClass)
	if err != nil || lockedFence+1 != claim.newFence {
		return ToolClaim{}, ErrToolNotClaimable
	}
	if hasEffect != (toolEffectClass != "read_only") || hasEffect && (effectClass != toolEffectClass || effectStatus != "executing" || providerRequestID == "") {
		return ToolClaim{}, ErrClaimConflict
	}
	if hasEffect && effectClass != "idempotent_write" {
		return ToolClaim{}, ErrToolEffectNeedsReconciliation
	}
	var cancelRequested *time.Time
	var dueAt time.Time
	if err = tx.QueryRow(ctx, `SELECT cancel_requested_at,due_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, runID, command.Command.TenantID).Scan(&cancelRequested, &dueAt); err != nil || cancelRequested != nil || !dueAt.After(claim.now) {
		return ToolClaim{}, ErrToolNotClaimable
	}
	var jobID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM agent.jobs WHERE tenant_id=$1 AND command_id=$2 AND status='running' FOR UPDATE`, command.Command.TenantID, command.Command.CommandID).Scan(&jobID); err != nil {
		return ToolClaim{}, ErrToolNotClaimable
	}
	var oldAttemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at<=$8 FOR UPDATE`, claim.oldAttemptID, command.Command.TenantID, jobID, command.Command.CommandID, claim.oldFence, claim.oldDigest, claim.oldExpiry, claim.now).Scan(&oldAttemptVersion)
	if err != nil {
		return ToolClaim{}, ErrToolNotClaimable
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='expired',finished_at=$2,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND status='running' AND fence=$6 AND lease_token_hash=$7 AND lease_expires_at=$8`, oldAttemptVersion+1, claim.now, claim.oldAttemptID, command.Command.TenantID, oldAttemptVersion, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return ToolClaim{}, ErrClaimConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET owner_attempt_id=$1,fence=$2,lease_token_hash=$3,lease_expires_at=$4,updated_at=$5 WHERE id=$6 AND tenant_id=$7 AND store_epoch=$8 AND consumer_name=$9 AND command_id=$10 AND request_hash=$11 AND status='running' AND owner_attempt_id=$12 AND fence=$13 AND lease_token_hash=$14 AND lease_expires_at=$15 AND lease_expires_at<=$5`, claim.newAttemptID, claim.newFence, claim.newDigest, claim.newExpiry, claim.now, claim.inboxID, command.Command.TenantID, command.Command.StoreEpoch, command.ConsumerName, command.Command.CommandID, command.Command.PayloadHash, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return ToolClaim{}, ErrClaimConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET active_attempt_id=$1,current_fence=$2,lease_token_hash=$3,lease_expires_at=$4,updated_at=$5 WHERE id=$6 AND tenant_id=$7 AND status='executing' AND tool_call_version=$8 AND active_command_id=$9 AND active_attempt_id=$10 AND current_fence=$11 AND lease_token_hash=$12 AND lease_expires_at=$13 AND lease_expires_at<=$5`, claim.newAttemptID, claim.newFence, claim.newDigest, claim.newExpiry, claim.now, command.Command.AggregateID, command.Command.TenantID, toolVersion, command.Command.CommandID, claim.oldAttemptID, claim.oldFence, claim.oldDigest, claim.oldExpiry); updateErr != nil || tag.RowsAffected() != 1 {
		return ToolClaim{}, ErrClaimConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'running',$9,$9,$9)`, claim.newAttemptID, command.Command.TenantID, jobID, command.Command.CommandID, claim.newFence, claim.newDigest, claim.newExpiry, command.WorkerID, claim.now); err != nil {
		return ToolClaim{}, err
	}
	if hasEffect {
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=$1,execution_attempt_id=$2,execution_fence=$3,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND version=$7 AND status='executing' AND effect_class='idempotent_write' AND provider_request_id=$8 AND execution_fence<$3`, effectVersion+1, claim.newAttemptID, claim.newFence, claim.now, effectID, command.Command.TenantID, effectVersion, providerRequestID); updateErr != nil || tag.RowsAffected() != 1 {
			return ToolClaim{}, ErrClaimConflict
		}
	}
	completedIDs, err := store.completionEventIdentifiers(claim.oldAttemptID, statemachine.RunExpired)
	if err != nil {
		return ToolClaim{}, err
	}
	startedIDs, err := store.toolClaimEventIdentifiers(claim.newAttemptID)
	if err != nil {
		return ToolClaim{}, err
	}
	oldStartedIDs, err := store.toolClaimEventIdentifiers(claim.oldAttemptID)
	if err != nil {
		return ToolClaim{}, err
	}
	oldCausationID := oldStartedIDs.attemptEvent
	completed := eventpostgres.Input{Event: eventpostgres.Event{ID: completedIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.oldAttemptID, AggregateVersion: oldAttemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: claim.now, Actor: command.Actor, CausationID: &oldCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completedIDs.attemptOutbox, CommandID: completedIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, completed); err != nil {
		return ToolClaim{}, err
	}
	newCausationID := completedIDs.attemptEvent
	started := eventpostgres.Input{Event: eventpostgres.Event{ID: startedIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: userID, EventType: "JobAttemptStarted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.newAttemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: claim.now, Actor: command.Actor, CausationID: &newCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: startedIDs.attemptOutbox, CommandID: startedIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, started); err != nil {
		return ToolClaim{}, err
	}
	return ToolClaim{ToolCallID: command.Command.AggregateID, RunID: runID, GroupID: groupID, TenantID: command.Command.TenantID, UserID: userID, StoreEpoch: command.Command.StoreEpoch, ToolCallVersion: toolVersion, EffectID: effectID, EffectClass: toolEffectClass, ProviderRequestID: providerRequestID, CommandID: command.Command.CommandID, ConsumerName: command.ConsumerName, RequestHash: command.Command.PayloadHash, JobID: jobID, InboxID: claim.inboxID, AttemptID: claim.newAttemptID, Fence: claim.newFence, LeaseToken: claim.newToken, LeaseExpiresAt: claim.newExpiry}, nil
}
