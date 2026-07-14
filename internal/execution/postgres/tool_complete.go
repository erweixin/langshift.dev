package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

type CompleteToolCommand struct {
	Claim                 ToolClaim
	ExpectedToolVersion   uint64
	TargetState           statemachine.ToolCallState
	ResultHash            string
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

type EffectCompletion struct {
	ExternalResourceRef string
	ReconciliationDueAt time.Time
	ReconcileCommand    PayloadPointer
	ReconcileQueueClass string
	ReconcileResource   string
	ReconcilePriority   int
	ReconcileCostUnits  int64
	ReconcileAttempts   int
}

type CompletedTool struct {
	ToolCallID, GroupID, RunID  string
	ToolCallVersion, RunVersion uint64
	Status                      statemachine.ToolCallState
	CompletedAt                 time.Time
	ContinuationID              string
	ResumeCommandID             string
	ReconcileCommandID          string
	Resumed                     bool
}

type continuationIDs struct {
	continuation, command, job, groupEvent, groupOutbox, groupPublish string
	runEvent, runOutbox, runPublish, resumeOutbox                     string
}

type reconciliationIDs struct{ outbox, command, job string }

// CompleteReadOnlyTool commits a deterministic read result and performs the
// protected parallel-group join in the same transaction. Effectful tools use
// the effect-ledger completion path and cannot pass this boundary.
func (store RunStore) CompleteReadOnlyTool(ctx context.Context, command CompleteToolCommand) (CompletedTool, error) {
	if command.Claim.EffectClass != "read_only" || command.TargetState == statemachine.ToolCallOutcomeUnknown {
		return CompletedTool{}, ErrInvalidCommand
	}
	return store.completeTool(ctx, command, nil, nil)
}

// CompleteEffectTool atomically records the durable effect outcome, ToolCall
// result, worker attempt, protected group join and any winning continuation.
func (store RunStore) CompleteEffectTool(ctx context.Context, command CompleteToolCommand, effect EffectCompletion) (CompletedTool, error) {
	if !isWriteEffectClass(command.Claim.EffectClass) || !validEffectCompletion(command.TargetState, effect) {
		return CompletedTool{}, ErrInvalidCommand
	}
	return store.completeTool(ctx, command, &effect, nil)
}

type toolCompletionHook func(context.Context, pgx.Tx, string, time.Time) error

func (store RunStore) completeTool(ctx context.Context, command CompleteToolCommand, effect *EffectCompletion, hook toolCompletionHook) (CompletedTool, error) {
	claim := command.Claim
	if !store.validClaim() || !validToolClaim(claim) {
		return CompletedTool{}, ErrConfiguration
	}
	if !validCompleteTool(command) {
		return CompletedTool{}, ErrInvalidCommand
	}
	if err := statemachine.ToolCalls.ValidateTransition(statemachine.ToolCallExecuting, command.TargetState); err != nil {
		return CompletedTool{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return CompletedTool{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	toolEventID, err := ids.DeterministicUUID(store.IDKey, "tool-call-completed-event", claim.AttemptID+"\x00"+string(command.TargetState))
	if err != nil {
		return CompletedTool{}, err
	}
	completionIDs, err := store.completionEventIdentifiers(claim.AttemptID, statemachine.RunState(command.TargetState))
	if err != nil {
		return CompletedTool{}, err
	}
	toolOutbox, err := ids.DeterministicUUID(store.IDKey, "tool-call-completed-publish-outbox", toolEventID)
	if err != nil {
		return CompletedTool{}, err
	}
	toolPublish, err := ids.DeterministicUUID(store.IDKey, "tool-call-completed-publish-command", toolEventID)
	if err != nil {
		return CompletedTool{}, err
	}
	var reconcile reconciliationIDs
	if command.TargetState == statemachine.ToolCallOutcomeUnknown {
		reconcile, err = store.reconciliationIdentifiers(claim.EffectID, command.ExpectedToolVersion+1)
		if err != nil {
			return CompletedTool{}, err
		}
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
	if err = lockToolInbox(ctx, tx, claim, digest[:], now); err != nil {
		return CompletedTool{}, err
	}
	var effectVersion uint64
	if effect == nil {
		var unexpectedEffectID string
		effectErr := tx.QueryRow(ctx, `SELECT id::text FROM agent.tool_effects WHERE tenant_id=$1 AND tool_call_id=$2 FOR UPDATE`, claim.TenantID, claim.ToolCallID).Scan(&unexpectedEffectID)
		if effectErr == nil || !errors.Is(effectErr, pgx.ErrNoRows) {
			return CompletedTool{}, ErrExecutionRightConflict
		}
	} else {
		var effectStatus, effectClass, effectAttemptID, providerRequestID string
		var effectFence uint64
		err = tx.QueryRow(ctx, `SELECT version,status,effect_class,execution_attempt_id::text,execution_fence,provider_request_id FROM agent.tool_effects WHERE id=$1 AND tenant_id=$2 AND tool_call_id=$3 AND run_id=$4 FOR UPDATE`, claim.EffectID, claim.TenantID, claim.ToolCallID, claim.RunID).Scan(&effectVersion, &effectStatus, &effectClass, &effectAttemptID, &effectFence, &providerRequestID)
		if err != nil || effectStatus != "executing" || effectClass != claim.EffectClass || effectAttemptID != claim.AttemptID || effectFence != claim.Fence || providerRequestID != claim.ProviderRequestID {
			return CompletedTool{}, ErrExecutionRightConflict
		}
	}
	var userID, runID, effectClass string
	err = tx.QueryRow(ctx, `SELECT user_id::text,run_id::text,effect_class FROM agent.tool_calls WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND run_id=$4 AND status='executing' AND tool_call_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$11 FOR UPDATE`, claim.ToolCallID, claim.TenantID, claim.UserID, claim.RunID, command.ExpectedToolVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&userID, &runID, &effectClass)
	if err != nil || effectClass != claim.EffectClass || runID != claim.RunID || (effect == nil && effectClass != "read_only") || (effect != nil && !isWriteEffectClass(effectClass)) {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	nextToolVersion := command.ExpectedToolVersion + 1
	if effect != nil {
		effectStatus, confirmedAt, reconciliationDueAt, externalResourceRef, valid := effectCompletionValues(command.TargetState, *effect, now)
		if !valid {
			return CompletedTool{}, ErrInvalidCommand
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=$1,status=$2,external_resource_ref=COALESCE(NULLIF($3,''),external_resource_ref),reconciliation_due_at=$4,confirmed_at=$5,result_event_id=$6,updated_at=$7 WHERE id=$8 AND tenant_id=$9 AND version=$10 AND status='executing' AND effect_class=$11 AND execution_attempt_id=$12 AND execution_fence=$13 AND provider_request_id=$14`, effectVersion+1, effectStatus, externalResourceRef, reconciliationDueAt, confirmedAt, toolEventID, now, claim.EffectID, claim.TenantID, effectVersion, claim.EffectClass, claim.AttemptID, claim.Fence, claim.ProviderRequestID); updateErr != nil || tag.RowsAffected() != 1 {
			return CompletedTool{}, ErrExecutionRightConflict
		}
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status=$1,tool_call_version=$2,result_event_id=$3,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND status='executing' AND tool_call_version=$7 AND active_command_id=$8 AND active_attempt_id=$9 AND current_fence=$10 AND lease_token_hash=$11 AND lease_expires_at=$12 AND lease_expires_at>$4`, command.TargetState, nextToolVersion, toolEventID, now, claim.ToolCallID, claim.TenantID, command.ExpectedToolVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	attemptStatus, jobStatus := "succeeded", "succeeded"
	if command.TargetState == statemachine.ToolCallFailed {
		attemptStatus, jobStatus = "failed", "failed"
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status=$2,finished_at=$3,result_hash=$4,updated_at=$3 WHERE id=$5 AND tenant_id=$6 AND version=$7 AND job_id=$8 AND command_id=$9 AND status='running' AND fence=$10 AND lease_token_hash=$11 AND lease_expires_at=$12 AND lease_expires_at>$3`, attemptVersion+1, attemptStatus, now, command.ResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND command_id=$5 AND status='running'`, jobStatus, now, claim.JobID, claim.TenantID, claim.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedTool{}, ErrExecutionRightConflict
	}

	result := CompletedTool{ToolCallID: claim.ToolCallID, GroupID: claim.GroupID, RunID: claim.RunID, ToolCallVersion: nextToolVersion, Status: command.TargetState, CompletedAt: now, ReconcileCommandID: reconcile.command}
	join, err := store.joinCompletedTool(ctx, tx, toolJoinInput{TenantID: claim.TenantID, UserID: userID, RunID: claim.RunID, GroupID: claim.GroupID, ToolEventID: toolEventID, Actor: command.Actor, CorrelationID: command.CorrelationID, GroupJoinedEvent: command.GroupJoinedEvent, RunResumeQueuedEvent: command.RunResumeQueuedEvent, ResumeCommand: command.ResumeCommand, ResumeQueueClass: command.ResumeQueueClass, ResumeResourceClass: command.ResumeResourceClass, ResumePriority: command.ResumePriority, ResumeCostUnits: command.ResumeCostUnits, ResumeMaxAttempts: command.ResumeMaxAttempts, Now: now})
	if err != nil {
		return CompletedTool{}, err
	}
	result.RunVersion, result.ContinuationID, result.ResumeCommandID, result.Resumed = join.RunVersion, join.ContinuationID, join.ResumeCommandID, join.Resumed

	causationID := claim.CommandID
	toolCommands := []eventpostgres.OutboxCommand{{ID: toolOutbox, CommandID: toolPublish, CommandType: "events.publish", PayloadRef: command.ToolCompletedEvent.Ref, PayloadHash: command.ToolCompletedEvent.Hash}}
	if effect != nil && command.TargetState == statemachine.ToolCallOutcomeUnknown {
		toolCommands = append(toolCommands, eventpostgres.OutboxCommand{ID: reconcile.outbox, CommandID: reconcile.command, CommandType: "ReconcileToolEffect", PayloadRef: effect.ReconcileCommand.Ref, PayloadHash: effect.ReconcileCommand.Hash})
	}
	toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: toolEventID, TenantID: claim.TenantID, UserID: userID, EventType: toolCompletionEventType(command.TargetState), SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: claim.ToolCallID, AggregateVersion: nextToolVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.ToolCompletedEvent.Ref, PayloadHash: command.ToolCompletedEvent.Hash}, Commands: toolCommands}
	if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
		return CompletedTool{}, err
	}
	if hook != nil {
		if err = hook(ctx, tx, toolEventID, now); err != nil {
			return CompletedTool{}, err
		}
	}
	if effect != nil && command.TargetState == statemachine.ToolCallOutcomeUnknown {
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.outbox SET available_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='pending'`, effect.ReconciliationDueAt, reconcile.outbox, claim.TenantID, reconcile.command); updateErr != nil || tag.RowsAffected() != 1 {
			return CompletedTool{}, ErrExecutionRightConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$10,$10)`, reconcile.job, claim.TenantID, reconcile.command, effect.ReconcileQueueClass, effect.ReconcileResource, effect.ReconcilePriority, effect.ReconcileCostUnits, effect.ReconcileAttempts, effect.ReconciliationDueAt, now); err != nil {
			return CompletedTool{}, err
		}
	}
	attemptCausationID := toolEventID
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: completionIDs.attemptEvent, TenantID: claim.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &attemptCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completionIDs.attemptOutbox, CommandID: completionIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}}}
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
	return result, nil
}

func validCompleteTool(command CompleteToolCommand) bool {
	knownOutcome := command.TargetState == statemachine.ToolCallSucceeded || command.TargetState == statemachine.ToolCallFailed || command.TargetState == statemachine.ToolCallOutcomeUnknown
	return command.ExpectedToolVersion == command.Claim.ToolCallVersion && knownOutcome && command.ResultHash != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ToolCompletedEvent) && validPointer(command.AttemptCompletedEvent) && validPointer(command.GroupJoinedEvent) && validPointer(command.RunResumeQueuedEvent) && validPointer(command.ResumeCommand) && (command.ResumeQueueClass == "interactive" || command.ResumeQueueClass == "background") && command.ResumeResourceClass != "" && command.ResumePriority >= 0 && command.ResumePriority <= 1000 && command.ResumeCostUnits > 0 && command.ResumeCostUnits <= 1_000_000_000_000 && command.ResumeMaxAttempts > 0 && command.ResumeMaxAttempts <= 100
}

func isWriteEffectClass(class string) bool {
	return class == "idempotent_write" || class == "reconcilable_write" || class == "compensatable_write" || class == "irreversible_write"
}

func validEffectCompletion(state statemachine.ToolCallState, effect EffectCompletion) bool {
	reconcileConfigured := validPointer(effect.ReconcileCommand) && (effect.ReconcileQueueClass == "interactive" || effect.ReconcileQueueClass == "background") && effect.ReconcileResource != "" && effect.ReconcilePriority >= 0 && effect.ReconcilePriority <= 1000 && effect.ReconcileCostUnits > 0 && effect.ReconcileCostUnits <= 1_000_000_000_000 && effect.ReconcileAttempts > 0 && effect.ReconcileAttempts <= 100
	if state == statemachine.ToolCallOutcomeUnknown {
		return !effect.ReconciliationDueAt.IsZero() && reconcileConfigured
	}
	return effect.ReconciliationDueAt.IsZero() && effect.ReconcileCommand == (PayloadPointer{}) && effect.ReconcileQueueClass == "" && effect.ReconcileResource == "" && effect.ReconcilePriority == 0 && effect.ReconcileCostUnits == 0 && effect.ReconcileAttempts == 0
}

func effectCompletionValues(state statemachine.ToolCallState, effect EffectCompletion, now time.Time) (string, *time.Time, *time.Time, string, bool) {
	switch state {
	case statemachine.ToolCallSucceeded:
		return "confirmed", &now, nil, effect.ExternalResourceRef, effect.ReconciliationDueAt.IsZero()
	case statemachine.ToolCallFailed:
		return "failed", nil, nil, "", effect.ReconciliationDueAt.IsZero() && effect.ExternalResourceRef == ""
	case statemachine.ToolCallOutcomeUnknown:
		return "outcome_unknown", nil, &effect.ReconciliationDueAt, effect.ExternalResourceRef, effect.ReconciliationDueAt.After(now)
	default:
		return "", nil, nil, "", false
	}
}

func parallelJoinSatisfied(policy string, required, quorum, terminal, succeeded int) bool {
	if required < 1 || terminal > required || succeeded > terminal {
		return false
	}
	switch policy {
	case "all":
		return terminal == required
	case "any":
		return succeeded > 0 || terminal == required
	case "quorum":
		return succeeded >= quorum || succeeded+required-terminal < quorum
	default:
		return false
	}
}

func toolCompletionEventType(state statemachine.ToolCallState) string {
	if state == statemachine.ToolCallSucceeded {
		return "ToolCallSucceeded"
	}
	if state == statemachine.ToolCallOutcomeUnknown {
		return "ToolCallOutcomeUnknown"
	}
	return "ToolCallFailed"
}

func (store RunStore) continuationIdentifiers(groupID string) (continuationIDs, error) {
	domains := []string{"parallel-continuation", "resume-agent-command", "resume-agent-job", "tool-group-joined-event", "tool-group-joined-publish-outbox", "tool-group-joined-publish-command", "run-resume-queued-event", "run-resume-queued-publish-outbox", "run-resume-queued-publish-command", "resume-agent-outbox"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, fmt.Sprintf("%s\x00resume", groupID))
		if err != nil {
			return continuationIDs{}, err
		}
		values[index] = value
	}
	return continuationIDs{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8], values[9]}, nil
}

func (store RunStore) reconciliationIdentifiers(effectID string, toolVersion uint64) (reconciliationIDs, error) {
	scope := fmt.Sprintf("%s\x00%d", effectID, toolVersion)
	domains := []string{"reconcile-tool-effect-outbox", "reconcile-tool-effect-command", "reconcile-tool-effect-job"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, scope)
		if err != nil {
			return reconciliationIDs{}, err
		}
		values[index] = value
	}
	return reconciliationIDs{values[0], values[1], values[2]}, nil
}
