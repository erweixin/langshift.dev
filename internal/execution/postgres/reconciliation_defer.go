package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

type DeferReconciliationCommand struct {
	Claim                 ReconciliationClaim
	ExpectedEffectVersion uint64
	ResultHash            string
	NextDueAt             time.Time
	Actor                 json.RawMessage
	CorrelationID         string
	AttemptCompletedEvent PayloadPointer
	NextReconcileCommand  PayloadPointer
	QueueClass            string
	ResourceClass         string
	Priority              int
	CostUnits             int64
	MaxAttempts           int
}

type DeferredReconciliation struct {
	ToolCallID, EffectID, NextCommandID, NextJobID string
	EffectVersion                                  uint64
	DueAt                                          time.Time
}

type reconciliationDeferralHook func(context.Context, pgx.Tx, string, time.Time) error

type reconciliationRetryIDs struct{ outbox, command, job string }

// DeferReconciliation records a completed-but-inconclusive provider lookup and
// schedules a fresh command without changing ToolCall state or result evidence.
func (store RunStore) DeferReconciliation(ctx context.Context, command DeferReconciliationCommand) (DeferredReconciliation, error) {
	return store.deferReconciliation(ctx, command, nil)
}

func (store RunStore) deferReconciliation(ctx context.Context, command DeferReconciliationCommand, hook reconciliationDeferralHook) (DeferredReconciliation, error) {
	claim := command.Claim
	if !store.validClaim() || !validReconciliationClaim(claim) {
		return DeferredReconciliation{}, ErrConfiguration
	}
	if !validDeferReconciliation(command) {
		return DeferredReconciliation{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return DeferredReconciliation{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	nextEffectVersion := command.ExpectedEffectVersion + 1
	retryIDs, err := store.reconciliationRetryIdentifiers(claim.EffectID, nextEffectVersion)
	if err != nil {
		return DeferredReconciliation{}, err
	}
	attemptEventIDs, err := store.completionEventIdentifiers(claim.AttemptID, statemachine.RunState(statemachine.ToolCallOutcomeUnknown))
	if err != nil {
		return DeferredReconciliation{}, err
	}
	now := store.claimNow()
	if !command.NextDueAt.After(now) || !command.NextDueAt.After(claim.ReconciliationDueAt) {
		return DeferredReconciliation{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return DeferredReconciliation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return DeferredReconciliation{}, err
	}
	if err = lockReconciliationInbox(ctx, tx, claim, digest[:], now); err != nil {
		return DeferredReconciliation{}, err
	}
	var effectVersion uint64
	var effectStatus, providerRequestID string
	var currentDueAt time.Time
	err = tx.QueryRow(ctx, `SELECT version,status,provider_request_id,reconciliation_due_at FROM agent.tool_effects WHERE id=$1 AND tenant_id=$2 AND tool_call_id=$3 AND run_id=$4 FOR UPDATE`, claim.EffectID, claim.TenantID, claim.ToolCallID, claim.RunID).Scan(&effectVersion, &effectStatus, &providerRequestID, &currentDueAt)
	if err != nil || effectVersion != command.ExpectedEffectVersion || effectStatus != "outcome_unknown" || providerRequestID != claim.ProviderRequestID || !currentDueAt.Equal(claim.ReconciliationDueAt) {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	var toolVersion uint64
	var toolStatus string
	err = tx.QueryRow(ctx, `SELECT tool_call_version,status FROM agent.tool_calls WHERE id=$1 AND tenant_id=$2 AND run_id=$3 FOR UPDATE`, claim.ToolCallID, claim.TenantID, claim.RunID).Scan(&toolVersion, &toolStatus)
	if err != nil || toolVersion != claim.ToolCallVersion || toolStatus != string(statemachine.ToolCallOutcomeUnknown) {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=$1,reconciliation_due_at=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND status='outcome_unknown' AND provider_request_id=$7 AND reconciliation_due_at=$8`, nextEffectVersion, command.NextDueAt, now, claim.EffectID, claim.TenantID, effectVersion, claim.ProviderRequestID, currentDueAt); updateErr != nil || tag.RowsAffected() != 1 {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='succeeded',finished_at=$2,result_hash=$3,updated_at=$2 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND job_id=$7 AND command_id=$8 AND status='running' AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$2`, attemptVersion+1, now, command.ResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='running'`, now, claim.JobID, claim.TenantID, claim.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	causationID := claim.CommandID
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: attemptEventIDs.attemptEvent, TenantID: claim.TenantID, UserID: claim.UserID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: attemptEventIDs.attemptOutbox, CommandID: attemptEventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, {ID: retryIDs.outbox, CommandID: retryIDs.command, CommandType: "ReconcileToolEffect", TargetAggregateKind: "tool_call", TargetAggregateID: claim.ToolCallID, PayloadRef: command.NextReconcileCommand.Ref, PayloadHash: command.NextReconcileCommand.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return DeferredReconciliation{}, err
	}
	if hook != nil {
		if err = hook(ctx, tx, attemptEventIDs.attemptEvent, now); err != nil {
			return DeferredReconciliation{}, err
		}
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.outbox SET available_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND aggregate_kind='tool_call' AND aggregate_id=$5 AND status='pending'`, command.NextDueAt, retryIDs.outbox, claim.TenantID, retryIDs.command, claim.ToolCallID); updateErr != nil || tag.RowsAffected() != 1 {
		return DeferredReconciliation{}, ErrExecutionRightConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$10,$10)`, retryIDs.job, claim.TenantID, retryIDs.command, command.QueueClass, command.ResourceClass, command.Priority, command.CostUnits, command.MaxAttempts, command.NextDueAt, now); err != nil {
		return DeferredReconciliation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DeferredReconciliation{}, err
	}
	return DeferredReconciliation{ToolCallID: claim.ToolCallID, EffectID: claim.EffectID, NextCommandID: retryIDs.command, NextJobID: retryIDs.job, EffectVersion: nextEffectVersion, DueAt: command.NextDueAt}, nil
}

func validDeferReconciliation(command DeferReconciliationCommand) bool {
	return command.ExpectedEffectVersion == command.Claim.EffectVersion && command.ResultHash != "" && !command.NextDueAt.IsZero() && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.AttemptCompletedEvent) && validPointer(command.NextReconcileCommand) && (command.QueueClass == "interactive" || command.QueueClass == "background") && command.ResourceClass != "" && command.Priority >= 0 && command.Priority <= 1000 && command.CostUnits > 0 && command.CostUnits <= 1_000_000_000_000 && command.MaxAttempts > 0 && command.MaxAttempts <= 100
}

func (store RunStore) reconciliationRetryIdentifiers(effectID string, effectVersion uint64) (reconciliationRetryIDs, error) {
	scope := fmt.Sprintf("%s\x00%d", effectID, effectVersion)
	domains := []string{"reconcile-tool-effect-retry-outbox", "reconcile-tool-effect-retry-command", "reconcile-tool-effect-retry-job"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, scope)
		if err != nil {
			return reconciliationRetryIDs{}, err
		}
		values[index] = value
	}
	return reconciliationRetryIDs{values[0], values[1], values[2]}, nil
}
