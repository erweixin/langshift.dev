package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

type EscalateReconciliationCommand struct {
	Claim                 ReconciliationClaim
	ExpectedEffectVersion uint64
	ResultHash            string
	Actor                 json.RawMessage
	CorrelationID         string
	AttemptCompletedEvent PayloadPointer
}

type EscalatedReconciliation struct {
	ToolCallID, EffectID, AttemptID string
	ToolCallVersion, EffectVersion  uint64
	EscalatedAt                     time.Time
}

// EscalateReconciliation closes a bounded automatic lookup attempt without
// changing the uncertain ToolCall or effect. No replacement command is
// created: the audited, two-person ToolEffect Repair protocol is the only
// remaining convergence authority.
func (store RunStore) EscalateReconciliation(ctx context.Context, command EscalateReconciliationCommand) (EscalatedReconciliation, error) {
	claim := command.Claim
	if !store.validClaim() || !validReconciliationClaim(claim) {
		return EscalatedReconciliation{}, ErrConfiguration
	}
	if !validEscalateReconciliation(command) {
		return EscalatedReconciliation{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return EscalatedReconciliation{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return EscalatedReconciliation{}, ErrExecutionRightConflict
	}
	attemptEventIDs, err := store.completionEventIdentifiers(claim.AttemptID, statemachine.RunState(statemachine.ToolCallOutcomeUnknown))
	if err != nil {
		return EscalatedReconciliation{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return EscalatedReconciliation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return EscalatedReconciliation{}, err
	}
	if err = lockReconciliationInbox(ctx, tx, claim, digest[:], now); err != nil {
		return EscalatedReconciliation{}, err
	}
	var effectVersion uint64
	var effectStatus, effectClass, providerRequestID string
	var dueAt time.Time
	err = tx.QueryRow(ctx, `SELECT version,status,effect_class,provider_request_id,reconciliation_due_at FROM agent.tool_effects WHERE id=$1 AND tenant_id=$2 AND tool_call_id=$3 AND run_id=$4 FOR UPDATE`, claim.EffectID, claim.TenantID, claim.ToolCallID, claim.RunID).Scan(&effectVersion, &effectStatus, &effectClass, &providerRequestID, &dueAt)
	if err != nil || effectVersion != command.ExpectedEffectVersion || effectStatus != "outcome_unknown" || effectClass != claim.EffectClass || providerRequestID != claim.ProviderRequestID || !dueAt.Equal(claim.ReconciliationDueAt) {
		return EscalatedReconciliation{}, ErrExecutionRightConflict
	}
	var toolVersion uint64
	var toolStatus string
	err = tx.QueryRow(ctx, `SELECT tool_call_version,status FROM agent.tool_calls WHERE id=$1 AND tenant_id=$2 AND run_id=$3 FOR UPDATE`, claim.ToolCallID, claim.TenantID, claim.RunID).Scan(&toolVersion, &toolStatus)
	if err != nil || toolVersion != claim.ToolCallVersion || toolStatus != string(statemachine.ToolCallOutcomeUnknown) {
		return EscalatedReconciliation{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return EscalatedReconciliation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return EscalatedReconciliation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='succeeded',finished_at=$2,result_hash=$3,updated_at=$2 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND job_id=$7 AND command_id=$8 AND status='running' AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$2`, attemptVersion+1, now, command.ResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return EscalatedReconciliation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='running'`, now, claim.JobID, claim.TenantID, claim.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return EscalatedReconciliation{}, ErrExecutionRightConflict
	}
	causationID := claim.CommandID
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: attemptEventIDs.attemptEvent, TenantID: claim.TenantID, UserID: claim.UserID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: attemptEventIDs.attemptOutbox, CommandID: attemptEventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return EscalatedReconciliation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EscalatedReconciliation{}, err
	}
	return EscalatedReconciliation{ToolCallID: claim.ToolCallID, EffectID: claim.EffectID, AttemptID: claim.AttemptID, ToolCallVersion: toolVersion, EffectVersion: effectVersion, EscalatedAt: now}, nil
}

func validEscalateReconciliation(command EscalateReconciliationCommand) bool {
	return command.ExpectedEffectVersion == command.Claim.EffectVersion && command.ResultHash != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.AttemptCompletedEvent)
}
