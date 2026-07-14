package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

type CompleteReconciliationCommand struct {
	Claim                 ReconciliationClaim
	ExpectedToolVersion   uint64
	ExpectedEffectVersion uint64
	TargetState           statemachine.ToolCallState
	ResultHash            string
	ExternalResourceRef   string
	Actor                 json.RawMessage
	CorrelationID         string
	ToolCompletedEvent    PayloadPointer
	AttemptCompletedEvent PayloadPointer
	GroupJoinedEvent      PayloadPointer
	RunResumeQueuedEvent  PayloadPointer
	ResumeCommand         PayloadPointer
	ResumeQueueClass      string
	ResumeResourceClass   string
	ResumePriority        int
	ResumeCostUnits       int64
	ResumeMaxAttempts     int
}

// CompleteReconciliation replaces unknown evidence exactly once and commits
// the effect, ToolCall, attempt, group join and continuation atomically.
func (store RunStore) CompleteReconciliation(ctx context.Context, command CompleteReconciliationCommand) (CompletedTool, error) {
	claim := command.Claim
	if !store.validClaim() || !validReconciliationClaim(claim) {
		return CompletedTool{}, ErrConfiguration
	}
	if !validCompleteReconciliation(command) {
		return CompletedTool{}, ErrInvalidCommand
	}
	if err := statemachine.ToolCalls.ValidateTransition(statemachine.ToolCallOutcomeUnknown, command.TargetState); err != nil || !statemachine.ToolCalls.IsTerminal(command.TargetState) {
		return CompletedTool{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return CompletedTool{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	toolEventIDs, err := store.reconciliationCompletionIdentifiers(claim.AttemptID, command.TargetState)
	if err != nil {
		return CompletedTool{}, err
	}
	attemptEventIDs, err := store.completionEventIdentifiers(claim.AttemptID, statemachine.RunState(command.TargetState))
	if err != nil {
		return CompletedTool{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CompletedTool{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return CompletedTool{}, err
	}
	if err = lockReconciliationInbox(ctx, tx, claim, digest[:], now); err != nil {
		return CompletedTool{}, err
	}
	var effectStatus, effectClass, providerRequestID string
	var effectVersion uint64
	err = tx.QueryRow(ctx, `SELECT version,status,effect_class,provider_request_id FROM agent.tool_effects WHERE id=$1 AND tenant_id=$2 AND tool_call_id=$3 AND run_id=$4 FOR UPDATE`, claim.EffectID, claim.TenantID, claim.ToolCallID, claim.RunID).Scan(&effectVersion, &effectStatus, &effectClass, &providerRequestID)
	if err != nil || effectVersion != command.ExpectedEffectVersion || effectStatus != "outcome_unknown" || effectClass != claim.EffectClass || providerRequestID != claim.ProviderRequestID {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	var userID, runID, groupID, toolStatus, toolEffectClass string
	var toolVersion uint64
	err = tx.QueryRow(ctx, `SELECT t.user_id::text,t.run_id::text,m.group_id::text,t.status,t.tool_call_version,t.effect_class FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id WHERE t.id=$1 AND t.tenant_id=$2 FOR UPDATE OF t`, claim.ToolCallID, claim.TenantID).Scan(&userID, &runID, &groupID, &toolStatus, &toolVersion, &toolEffectClass)
	if err != nil || userID != claim.UserID || runID != claim.RunID || groupID != claim.GroupID || toolStatus != string(statemachine.ToolCallOutcomeUnknown) || toolVersion != command.ExpectedToolVersion || toolEffectClass != claim.EffectClass {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	effectOutcome, confirmedAt, externalResourceRef := reconciliationOutcome(command.TargetState, command.ExternalResourceRef, now)
	nextEffectVersion, nextToolVersion := effectVersion+1, toolVersion+1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=$1,status=$2,external_resource_ref=COALESCE(NULLIF($3,''),external_resource_ref),reconciliation_due_at=NULL,confirmed_at=$4,result_event_id=$5,updated_at=$6 WHERE id=$7 AND tenant_id=$8 AND version=$9 AND status='outcome_unknown' AND effect_class=$10 AND provider_request_id=$11`, nextEffectVersion, effectOutcome, externalResourceRef, confirmedAt, toolEventIDs.event, now, claim.EffectID, claim.TenantID, effectVersion, claim.EffectClass, claim.ProviderRequestID); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status=$1,tool_call_version=$2,result_event_id=$3,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND run_id=$7 AND status='outcome_unknown' AND tool_call_version=$8`, command.TargetState, nextToolVersion, toolEventIDs.event, now, claim.ToolCallID, claim.TenantID, claim.RunID, toolVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='succeeded',finished_at=$2,result_hash=$3,updated_at=$2 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND job_id=$7 AND command_id=$8 AND status='running' AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$2`, attemptVersion+1, now, command.ResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='running'`, now, claim.JobID, claim.TenantID, claim.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	join, err := store.joinCompletedTool(ctx, tx, toolJoinInput{TenantID: claim.TenantID, UserID: userID, RunID: claim.RunID, GroupID: claim.GroupID, ToolEventID: toolEventIDs.event, Actor: command.Actor, CorrelationID: command.CorrelationID, GroupJoinedEvent: command.GroupJoinedEvent, RunResumeQueuedEvent: command.RunResumeQueuedEvent, ResumeCommand: command.ResumeCommand, ResumeQueueClass: command.ResumeQueueClass, ResumeResourceClass: command.ResumeResourceClass, ResumePriority: command.ResumePriority, ResumeCostUnits: command.ResumeCostUnits, ResumeMaxAttempts: command.ResumeMaxAttempts, Now: now})
	if err != nil {
		return CompletedTool{}, err
	}
	causationID := claim.CommandID
	toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: toolEventIDs.event, TenantID: claim.TenantID, UserID: userID, EventType: toolCompletionEventType(command.TargetState), SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: claim.ToolCallID, AggregateVersion: nextToolVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.ToolCompletedEvent.Ref, PayloadHash: command.ToolCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: toolEventIDs.outbox, CommandID: toolEventIDs.publish, CommandType: "events.publish", PayloadRef: command.ToolCompletedEvent.Ref, PayloadHash: command.ToolCompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
		return CompletedTool{}, err
	}
	attemptCausationID := toolEventIDs.event
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: attemptEventIDs.attemptEvent, TenantID: claim.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &attemptCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: attemptEventIDs.attemptOutbox, CommandID: attemptEventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return CompletedTool{}, err
	}
	if join.GroupEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *join.GroupEvent); err != nil {
			return CompletedTool{}, err
		}
	}
	if join.RunEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *join.RunEvent); err != nil {
			return CompletedTool{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return CompletedTool{}, err
	}
	return CompletedTool{ToolCallID: claim.ToolCallID, GroupID: claim.GroupID, RunID: claim.RunID, ToolCallVersion: nextToolVersion, RunVersion: join.RunVersion, Status: command.TargetState, CompletedAt: now, ContinuationID: join.ContinuationID, ResumeCommandID: join.ResumeCommandID, Resumed: join.Resumed}, nil
}

type reconciliationCompletionIDs struct{ event, outbox, publish string }

func (store RunStore) reconciliationCompletionIdentifiers(attemptID string, state statemachine.ToolCallState) (reconciliationCompletionIDs, error) {
	domains := []string{"tool-effect-reconciled-event", "tool-effect-reconciled-publish-outbox", "tool-effect-reconciled-publish-command"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, attemptID+"\x00"+string(state))
		if err != nil {
			return reconciliationCompletionIDs{}, err
		}
		values[index] = value
	}
	return reconciliationCompletionIDs{values[0], values[1], values[2]}, nil
}

func validCompleteReconciliation(command CompleteReconciliationCommand) bool {
	terminal := command.TargetState == statemachine.ToolCallSucceeded || command.TargetState == statemachine.ToolCallFailed
	externalValid := command.TargetState == statemachine.ToolCallSucceeded || command.ExternalResourceRef == ""
	return command.ExpectedToolVersion == command.Claim.ToolCallVersion && command.ExpectedEffectVersion == command.Claim.EffectVersion && terminal && externalValid && command.ResultHash != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ToolCompletedEvent) && validPointer(command.AttemptCompletedEvent) && validPointer(command.GroupJoinedEvent) && validPointer(command.RunResumeQueuedEvent) && validPointer(command.ResumeCommand) && (command.ResumeQueueClass == "interactive" || command.ResumeQueueClass == "background") && command.ResumeResourceClass != "" && command.ResumePriority >= 0 && command.ResumePriority <= 1000 && command.ResumeCostUnits > 0 && command.ResumeCostUnits <= 1_000_000_000_000 && command.ResumeMaxAttempts > 0 && command.ResumeMaxAttempts <= 100
}

func reconciliationOutcome(state statemachine.ToolCallState, externalResourceRef string, now time.Time) (string, *time.Time, string) {
	if state == statemachine.ToolCallSucceeded {
		return "confirmed", &now, externalResourceRef
	}
	return "failed", nil, ""
}
