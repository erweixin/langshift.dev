package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	maximumExpiredEffectTenantPage = 5000
	maximumExpiredEffectPage       = 500
)

var ErrToolEffectNotSweepable = errors.New("expired tool effect is no longer sweepable")

type ExpiredToolEffectCandidate struct {
	TenantID, UserID, StoreEpoch, ToolCallID, RunID, EffectID string
	CommandID, AttemptID, InboxID, ConsumerName, RequestHash  string
	JobID, EffectClass, ProviderRequestID                     string
	ToolVersion, EffectVersion, AttemptVersion, Fence         uint64
	LeaseExpiresAt                                            time.Time
}

type SweepExpiredToolEffectCommand struct {
	Candidate           ExpiredToolEffectCandidate
	ResultHash          string
	Actor               json.RawMessage
	CorrelationID       string
	OutcomeUnknownEvent PayloadPointer
	AttemptExpiredEvent PayloadPointer
	Reconciliation      EffectCompletion
}

type SweptToolEffect struct {
	ToolCallID, EffectID, CommandID, AttemptID, EventID, ReconcileCommandID string
	ToolVersion, EffectVersion                                              uint64
	ReconciliationDueAt                                                     time.Time
}

func (store RunStore) ListExpiredEffectTenantIDs(ctx context.Context, storeEpoch, after string, limit, shardIndex, shardCount int) ([]string, error) {
	if !store.validClaim() || storeEpoch == "" || limit < 1 || limit > maximumExpiredEffectTenantPage || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount {
		return nil, ErrConfiguration
	}
	if err := store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_expired_tool_effect_tenants($1::uuid,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, storeEpoch, after, limit, shardIndex, shardCount, store.claimNow())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0, limit)
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (store RunStore) ListExpiredEffectCandidates(ctx context.Context, tenantID, storeEpoch, afterToolCallID string, limit int) ([]ExpiredToolEffectCandidate, error) {
	if !store.validClaim() || tenantID == "" || storeEpoch == "" || limit < 1 || limit > maximumExpiredEffectPage {
		return nil, ErrConfiguration
	}
	if err := store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	now := store.claimNow()
	rows, err := tx.Query(ctx, `SELECT t.tenant_id::text,t.user_id::text,o.store_epoch::text,t.id::text,t.run_id::text,e.id::text,
		t.active_command_id::text,t.active_attempt_id::text,i.id::text,i.consumer_name,i.request_hash,j.id::text,
		t.effect_class,e.provider_request_id,t.tool_call_version,e.version,a.version,t.current_fence,t.lease_expires_at
		FROM agent.tool_calls t
		JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id
		JOIN agent.outbox o ON o.tenant_id=t.tenant_id AND o.command_id=t.active_command_id
		JOIN agent.inbox i ON i.tenant_id=t.tenant_id AND i.command_id=t.active_command_id AND i.owner_attempt_id=t.active_attempt_id
		JOIN agent.jobs j ON j.tenant_id=t.tenant_id AND j.command_id=t.active_command_id
		JOIN agent.job_attempts a ON a.tenant_id=t.tenant_id AND a.id=t.active_attempt_id AND a.job_id=j.id AND a.command_id=j.command_id
		WHERE t.tenant_id=$1 AND o.store_epoch=$2 AND t.status='executing'
		  AND t.effect_class IN ('reconcilable_write','compensatable_write','irreversible_write') AND t.lease_expires_at<=$3
		  AND e.status='executing' AND e.execution_attempt_id=t.active_attempt_id AND e.execution_fence=t.current_fence AND NULLIF(e.provider_request_id,'') IS NOT NULL
		  AND i.status='running' AND i.fence=t.current_fence AND i.lease_expires_at=t.lease_expires_at AND i.lease_expires_at<=$3 AND i.request_hash=o.payload_hash
		  AND j.status='running' AND a.status='running' AND a.fence=t.current_fence AND a.lease_expires_at=t.lease_expires_at AND a.lease_expires_at<=$3
		  AND (NULLIF($4,'')::uuid IS NULL OR t.id>NULLIF($4,'')::uuid)
		ORDER BY t.id LIMIT $5`, tenantID, storeEpoch, now, afterToolCallID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ExpiredToolEffectCandidate, 0, limit)
	for rows.Next() {
		var candidate ExpiredToolEffectCandidate
		if err = rows.Scan(&candidate.TenantID, &candidate.UserID, &candidate.StoreEpoch, &candidate.ToolCallID, &candidate.RunID, &candidate.EffectID, &candidate.CommandID, &candidate.AttemptID, &candidate.InboxID, &candidate.ConsumerName, &candidate.RequestHash, &candidate.JobID, &candidate.EffectClass, &candidate.ProviderRequestID, &candidate.ToolVersion, &candidate.EffectVersion, &candidate.AttemptVersion, &candidate.Fence, &candidate.LeaseExpiresAt); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// SweepExpiredToolEffect fences an abandoned reconcilable execution and
// records uncertainty. It never invokes the provider and never replays the
// original command; the only follow-up is a provider-side reconciliation.
func (store RunStore) SweepExpiredToolEffect(ctx context.Context, command SweepExpiredToolEffectCommand) (SweptToolEffect, error) {
	candidate := command.Candidate
	if !store.validClaim() || !validSweepExpiredToolEffect(command) {
		return SweptToolEffect{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, candidate.StoreEpoch); err != nil {
		return SweptToolEffect{}, err
	}
	now := store.claimNow()
	if candidate.LeaseExpiresAt.After(now) || !command.Reconciliation.ReconciliationDueAt.After(now) {
		return SweptToolEffect{}, ErrToolEffectNotSweepable
	}
	nextToolVersion := candidate.ToolVersion + 1
	nextEffectVersion := candidate.EffectVersion + 1
	toolEventID, err := ids.DeterministicUUID(store.IDKey, "tool-call-completed-event", candidate.AttemptID+"\x00"+string(statemachine.ToolCallOutcomeUnknown))
	if err != nil {
		return SweptToolEffect{}, err
	}
	toolOutboxID, err := ids.DeterministicUUID(store.IDKey, "tool-call-completed-publish-outbox", toolEventID)
	if err != nil {
		return SweptToolEffect{}, err
	}
	toolPublishID, err := ids.DeterministicUUID(store.IDKey, "tool-call-completed-publish-command", toolEventID)
	if err != nil {
		return SweptToolEffect{}, err
	}
	attemptEventIDs, err := store.completionEventIdentifiers(candidate.AttemptID, statemachine.RunState(statemachine.ToolCallOutcomeUnknown))
	if err != nil {
		return SweptToolEffect{}, err
	}
	reconcile, err := store.reconciliationIdentifiers(candidate.EffectID, nextToolVersion)
	if err != nil {
		return SweptToolEffect{}, err
	}

	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return SweptToolEffect{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, candidate.TenantID); err != nil {
		return SweptToolEffect{}, err
	}
	if err = lockCommandDelivery(ctx, tx, candidate.CommandID); err != nil {
		return SweptToolEffect{}, err
	}
	var inboxStatus, inboxEpoch, inboxAttempt, inboxHash string
	var inboxFence uint64
	var inboxDigest []byte
	var inboxExpiry time.Time
	err = tx.QueryRow(ctx, `SELECT status,store_epoch::text,owner_attempt_id::text,fence,lease_token_hash,lease_expires_at,request_hash FROM agent.inbox WHERE id=$1 AND tenant_id=$2 AND consumer_name=$3 AND command_id=$4 FOR UPDATE`, candidate.InboxID, candidate.TenantID, candidate.ConsumerName, candidate.CommandID).Scan(&inboxStatus, &inboxEpoch, &inboxAttempt, &inboxFence, &inboxDigest, &inboxExpiry, &inboxHash)
	if err != nil || inboxStatus != "running" || inboxEpoch != candidate.StoreEpoch || inboxAttempt != candidate.AttemptID || inboxFence != candidate.Fence || !inboxExpiry.Equal(candidate.LeaseExpiresAt) || inboxExpiry.After(now) || inboxHash != candidate.RequestHash {
		return SweptToolEffect{}, ErrToolEffectNotSweepable
	}
	var effectVersion, effectFence uint64
	var effectStatus, effectClass, effectAttempt, providerRequestID string
	err = tx.QueryRow(ctx, `SELECT version,status,effect_class,execution_attempt_id::text,execution_fence,provider_request_id FROM agent.tool_effects WHERE id=$1 AND tenant_id=$2 AND tool_call_id=$3 AND run_id=$4 FOR UPDATE`, candidate.EffectID, candidate.TenantID, candidate.ToolCallID, candidate.RunID).Scan(&effectVersion, &effectStatus, &effectClass, &effectAttempt, &effectFence, &providerRequestID)
	if err != nil || effectVersion != candidate.EffectVersion || effectStatus != "executing" || effectClass != candidate.EffectClass || !isAutomaticallyReconcilableEffectClass(effectClass) || effectAttempt != candidate.AttemptID || effectFence != candidate.Fence || providerRequestID != candidate.ProviderRequestID {
		return SweptToolEffect{}, ErrToolEffectNotSweepable
	}
	var toolVersion, toolFence uint64
	var userID, runID, toolStatus, activeCommand, activeAttempt, toolEffectClass string
	var toolDigest []byte
	var toolExpiry time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,run_id::text,status,tool_call_version,active_command_id::text,active_attempt_id::text,current_fence,lease_token_hash,lease_expires_at,effect_class FROM agent.tool_calls WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, candidate.ToolCallID, candidate.TenantID).Scan(&userID, &runID, &toolStatus, &toolVersion, &activeCommand, &activeAttempt, &toolFence, &toolDigest, &toolExpiry, &toolEffectClass)
	if err != nil || userID != candidate.UserID || runID != candidate.RunID || toolStatus != "executing" || toolVersion != candidate.ToolVersion || activeCommand != candidate.CommandID || activeAttempt != candidate.AttemptID || toolFence != candidate.Fence || !toolExpiry.Equal(candidate.LeaseExpiresAt) || toolExpiry.After(now) || toolEffectClass != candidate.EffectClass || !equalBytes(toolDigest, inboxDigest) {
		return SweptToolEffect{}, ErrToolEffectNotSweepable
	}
	var attemptVersion, attemptFence uint64
	var attemptStatus string
	var attemptDigest []byte
	var attemptExpiry time.Time
	err = tx.QueryRow(ctx, `SELECT version,status,fence,lease_token_hash,lease_expires_at FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 FOR UPDATE`, candidate.AttemptID, candidate.TenantID, candidate.JobID, candidate.CommandID).Scan(&attemptVersion, &attemptStatus, &attemptFence, &attemptDigest, &attemptExpiry)
	if err != nil || attemptVersion != candidate.AttemptVersion || attemptStatus != "running" || attemptFence != candidate.Fence || !attemptExpiry.Equal(candidate.LeaseExpiresAt) || attemptExpiry.After(now) || !equalBytes(attemptDigest, inboxDigest) {
		return SweptToolEffect{}, ErrToolEffectNotSweepable
	}
	var jobStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM agent.jobs WHERE id=$1 AND tenant_id=$2 AND command_id=$3 FOR UPDATE`, candidate.JobID, candidate.TenantID, candidate.CommandID).Scan(&jobStatus)
	if err != nil || jobStatus != "running" {
		return SweptToolEffect{}, ErrToolEffectNotSweepable
	}

	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=$1,status='outcome_unknown',reconciliation_due_at=$2,result_event_id=$3,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND version=$7 AND status='executing' AND execution_attempt_id=$8 AND execution_fence=$9 AND provider_request_id=$10`, nextEffectVersion, command.Reconciliation.ReconciliationDueAt, toolEventID, now, candidate.EffectID, candidate.TenantID, candidate.EffectVersion, candidate.AttemptID, candidate.Fence, candidate.ProviderRequestID); updateErr != nil || tag.RowsAffected() != 1 {
		return SweptToolEffect{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status='outcome_unknown',tool_call_version=$1,result_event_id=$2,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND status='executing' AND tool_call_version=$6 AND active_command_id=$7 AND active_attempt_id=$8 AND current_fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at<=$3`, nextToolVersion, toolEventID, now, candidate.ToolCallID, candidate.TenantID, candidate.ToolVersion, candidate.CommandID, candidate.AttemptID, candidate.Fence, inboxDigest, candidate.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return SweptToolEffect{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND status='running' AND owner_attempt_id=$7 AND fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at<=$1 AND request_hash=$11`, now, candidate.InboxID, candidate.TenantID, candidate.StoreEpoch, candidate.ConsumerName, candidate.CommandID, candidate.AttemptID, candidate.Fence, inboxDigest, candidate.LeaseExpiresAt, candidate.RequestHash); updateErr != nil || tag.RowsAffected() != 1 {
		return SweptToolEffect{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='expired',finished_at=$2,result_hash=$3,updated_at=$2 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND job_id=$7 AND command_id=$8 AND status='running' AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at<=$2`, attemptVersion+1, now, command.ResultHash, candidate.AttemptID, candidate.TenantID, candidate.AttemptVersion, candidate.JobID, candidate.CommandID, candidate.Fence, inboxDigest, candidate.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return SweptToolEffect{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',delivery_due_at=NULL,redelivery_requested_at=NULL,redelivery_reason=NULL,dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='running'`, now, candidate.JobID, candidate.TenantID, candidate.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return SweptToolEffect{}, ErrExecutionRightConflict
	}

	causationID := candidate.CommandID
	toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: toolEventID, TenantID: candidate.TenantID, UserID: candidate.UserID, EventType: "ToolCallOutcomeUnknown", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: candidate.ToolCallID, AggregateVersion: nextToolVersion, StoreEpoch: candidate.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.OutcomeUnknownEvent.Ref, PayloadHash: command.OutcomeUnknownEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: toolOutboxID, CommandID: toolPublishID, CommandType: "events.publish", PayloadRef: command.OutcomeUnknownEvent.Ref, PayloadHash: command.OutcomeUnknownEvent.Hash}, {ID: reconcile.outbox, CommandID: reconcile.command, CommandType: "ReconcileToolEffect", PayloadRef: command.Reconciliation.ReconcileCommand.Ref, PayloadHash: command.Reconciliation.ReconcileCommand.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
		return SweptToolEffect{}, err
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.outbox SET available_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='pending'`, command.Reconciliation.ReconciliationDueAt, reconcile.outbox, candidate.TenantID, reconcile.command); updateErr != nil || tag.RowsAffected() != 1 {
		return SweptToolEffect{}, ErrExecutionRightConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$10,$10)`, reconcile.job, candidate.TenantID, reconcile.command, command.Reconciliation.ReconcileQueueClass, command.Reconciliation.ReconcileResource, command.Reconciliation.ReconcilePriority, command.Reconciliation.ReconcileCostUnits, command.Reconciliation.ReconcileAttempts, command.Reconciliation.ReconciliationDueAt, now); err != nil {
		return SweptToolEffect{}, err
	}
	attemptCausationID := toolEventID
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: attemptEventIDs.attemptEvent, TenantID: candidate.TenantID, UserID: candidate.UserID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: candidate.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: candidate.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &attemptCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: attemptEventIDs.attemptOutbox, CommandID: attemptEventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptExpiredEvent.Ref, PayloadHash: command.AttemptExpiredEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return SweptToolEffect{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SweptToolEffect{}, err
	}
	return SweptToolEffect{ToolCallID: candidate.ToolCallID, EffectID: candidate.EffectID, CommandID: candidate.CommandID, AttemptID: candidate.AttemptID, EventID: toolEventID, ReconcileCommandID: reconcile.command, ToolVersion: nextToolVersion, EffectVersion: nextEffectVersion, ReconciliationDueAt: command.Reconciliation.ReconciliationDueAt}, nil
}

func validSweepExpiredToolEffect(command SweepExpiredToolEffectCommand) bool {
	candidate := command.Candidate
	return candidate.TenantID != "" && candidate.UserID != "" && candidate.StoreEpoch != "" && candidate.ToolCallID != "" && candidate.RunID != "" && candidate.EffectID != "" && candidate.CommandID != "" && candidate.AttemptID != "" && candidate.InboxID != "" && candidate.ConsumerName != "" && candidate.RequestHash != "" && candidate.JobID != "" && isAutomaticallyReconcilableEffectClass(candidate.EffectClass) && candidate.ProviderRequestID != "" && candidate.ToolVersion > 0 && candidate.EffectVersion > 0 && candidate.AttemptVersion > 0 && candidate.Fence > 0 && !candidate.LeaseExpiresAt.IsZero() && command.ResultHash != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.OutcomeUnknownEvent) && validPointer(command.AttemptExpiredEvent) && validEffectCompletion(statemachine.ToolCallOutcomeUnknown, command.Reconciliation)
}

func isAutomaticallyReconcilableEffectClass(class string) bool {
	return class == "reconcilable_write"
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
