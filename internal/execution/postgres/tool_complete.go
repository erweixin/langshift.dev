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

type CompletedTool struct {
	ToolCallID, GroupID, RunID  string
	ToolCallVersion, RunVersion uint64
	Status                      statemachine.ToolCallState
	CompletedAt                 time.Time
	ContinuationID              string
	ResumeCommandID             string
	Resumed                     bool
}

type continuationIDs struct {
	continuation, command, job, groupEvent, groupOutbox, groupPublish string
	runEvent, runOutbox, runPublish, resumeOutbox                     string
}

// CompleteReadOnlyTool commits a deterministic read result and performs the
// protected parallel-group join in the same transaction. Effectful tools use
// the effect-ledger completion path and cannot pass this boundary.
func (store RunStore) CompleteReadOnlyTool(ctx context.Context, command CompleteToolCommand) (CompletedTool, error) {
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
	var userID, runID, effectClass string
	err = tx.QueryRow(ctx, `SELECT user_id::text,run_id::text,effect_class FROM agent.tool_calls WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND run_id=$4 AND status='executing' AND tool_call_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$11 FOR UPDATE`, claim.ToolCallID, claim.TenantID, claim.UserID, claim.RunID, command.ExpectedToolVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&userID, &runID, &effectClass)
	if err != nil || effectClass != "read_only" || runID != claim.RunID {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	nextToolVersion := command.ExpectedToolVersion + 1
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

	var joinPolicy, groupKind, continuationKind string
	var requiredCount, quorumCount int
	var groupVersion uint64
	var joined bool
	err = tx.QueryRow(ctx, `SELECT version,join_policy,required_count,quorum_count,group_kind,continuation_kind,joined FROM agent.parallel_groups WHERE id=$1 AND tenant_id=$2 AND run_id=$3 FOR UPDATE`, claim.GroupID, claim.TenantID, claim.RunID).Scan(&groupVersion, &joinPolicy, &requiredCount, &quorumCount, &groupKind, &continuationKind, &joined)
	if err != nil || groupKind != "execution" || continuationKind != "resume" {
		return CompletedTool{}, ErrExecutionRightConflict
	}
	var terminalCount, successCount int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE m.required AND t.status IN ('succeeded','failed','cancelled','resolved_unknown')),count(*) FILTER (WHERE m.required AND t.status='succeeded') FROM agent.parallel_group_members m JOIN agent.tool_calls t ON t.tenant_id=m.tenant_id AND t.id=m.tool_call_id WHERE m.tenant_id=$1 AND m.group_id=$2`, claim.TenantID, claim.GroupID).Scan(&terminalCount, &successCount)
	if err != nil {
		return CompletedTool{}, err
	}
	joinSatisfied := parallelJoinSatisfied(joinPolicy, requiredCount, quorumCount, terminalCount, successCount)
	result := CompletedTool{ToolCallID: claim.ToolCallID, GroupID: claim.GroupID, RunID: claim.RunID, ToolCallVersion: nextToolVersion, Status: command.TargetState, CompletedAt: now}
	var groupEvent *eventpostgres.Input
	var runEvent *eventpostgres.Input
	if joinSatisfied && !joined {
		var runVersion uint64
		var runStatus string
		var cancelRequested *time.Time
		var dueAt time.Time
		err = tx.QueryRow(ctx, `SELECT run_version,status,cancel_requested_at,due_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, claim.RunID, claim.TenantID).Scan(&runVersion, &runStatus, &cancelRequested, &dueAt)
		if err != nil {
			return CompletedTool{}, err
		}
		if runStatus == string(statemachine.RunWaitingTool) && cancelRequested == nil && dueAt.After(now) {
			continuation, idErr := store.continuationIdentifiers(claim.GroupID)
			if idErr != nil {
				return CompletedTool{}, idErr
			}
			tag, insertErr := tx.Exec(ctx, `INSERT INTO agent.continuations(id,tenant_id,run_id,run_version,group_kind,group_id,command_id,status,continuation_kind,created_at,updated_at) VALUES($1,$2,$3,$4,'parallel',$5,$6,'preparing','resume',$7,$7) ON CONFLICT DO NOTHING`, continuation.continuation, claim.TenantID, claim.RunID, runVersion, claim.GroupID, continuation.command, now)
			if insertErr != nil {
				return CompletedTool{}, insertErr
			}
			if tag.RowsAffected() != 1 {
				return CompletedTool{}, ErrExecutionRightConflict
			}
			{
				nextRunVersion := runVersion + 1
				if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='queued',run_version=$1,pending_command_id=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND status='waiting_tool' AND run_version=$6 AND cancel_requested_at IS NULL AND due_at>$3`, nextRunVersion, continuation.command, now, claim.RunID, claim.TenantID, runVersion); updateErr != nil || tag.RowsAffected() != 1 {
					return CompletedTool{}, ErrExecutionRightConflict
				}
				if tag, updateErr := tx.Exec(ctx, `UPDATE agent.continuations SET version=version+1,status='committed',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND status='preparing'`, now, continuation.continuation, claim.TenantID); updateErr != nil || tag.RowsAffected() != 1 {
					return CompletedTool{}, ErrExecutionRightConflict
				}
				if tag, updateErr := tx.Exec(ctx, `UPDATE agent.parallel_groups SET version=version+1,joined=true,continuation_id=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND joined=false AND continuation_id IS NULL`, continuation.continuation, now, claim.GroupID, claim.TenantID, groupVersion); updateErr != nil || tag.RowsAffected() != 1 {
					return CompletedTool{}, ErrExecutionRightConflict
				}
				if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9)`, continuation.job, claim.TenantID, continuation.command, command.ResumeQueueClass, command.ResumeResourceClass, command.ResumePriority, command.ResumeCostUnits, command.ResumeMaxAttempts, now, dueAt); err != nil {
					return CompletedTool{}, err
				}
				causationID := toolEventID
				group := eventpostgres.Input{Event: eventpostgres.Event{ID: continuation.groupEvent, TenantID: claim.TenantID, UserID: userID, EventType: "ToolGroupJoined", SchemaVersion: 1, AggregateKind: "parallel_group", AggregateID: claim.GroupID, AggregateVersion: groupVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.GroupJoinedEvent.Ref, PayloadHash: command.GroupJoinedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: continuation.groupOutbox, CommandID: continuation.groupPublish, CommandType: "events.publish", PayloadRef: command.GroupJoinedEvent.Ref, PayloadHash: command.GroupJoinedEvent.Hash}}}
				groupEvent = &group
				groupCausationID := continuation.groupEvent
				resume := eventpostgres.Input{Event: eventpostgres.Event{ID: continuation.runEvent, TenantID: claim.TenantID, UserID: userID, EventType: "RunResumeQueued", SchemaVersion: 1, AggregateKind: "run", AggregateID: claim.RunID, AggregateVersion: nextRunVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &groupCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.RunResumeQueuedEvent.Ref, PayloadHash: command.RunResumeQueuedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: continuation.runOutbox, CommandID: continuation.runPublish, CommandType: "events.publish", PayloadRef: command.RunResumeQueuedEvent.Ref, PayloadHash: command.RunResumeQueuedEvent.Hash}, {ID: continuation.resumeOutbox, CommandID: continuation.command, CommandType: "ResumeAgentRun", PayloadRef: command.ResumeCommand.Ref, PayloadHash: command.ResumeCommand.Hash}}}
				runEvent = &resume
				result.RunVersion, result.ContinuationID, result.ResumeCommandID, result.Resumed = nextRunVersion, continuation.continuation, continuation.command, true
			}
		}
	}

	causationID := claim.CommandID
	toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: toolEventID, TenantID: claim.TenantID, UserID: userID, EventType: toolCompletionEventType(command.TargetState), SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: claim.ToolCallID, AggregateVersion: nextToolVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.ToolCompletedEvent.Ref, PayloadHash: command.ToolCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: toolOutbox, CommandID: toolPublish, CommandType: "events.publish", PayloadRef: command.ToolCompletedEvent.Ref, PayloadHash: command.ToolCompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
		return CompletedTool{}, err
	}
	attemptCausationID := toolEventID
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: completionIDs.attemptEvent, TenantID: claim.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &attemptCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completionIDs.attemptOutbox, CommandID: completionIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return CompletedTool{}, err
	}
	if groupEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *groupEvent); err != nil {
			return CompletedTool{}, err
		}
	}
	if runEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *runEvent); err != nil {
			return CompletedTool{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return CompletedTool{}, err
	}
	return result, nil
}

func validCompleteTool(command CompleteToolCommand) bool {
	return command.ExpectedToolVersion == command.Claim.ToolCallVersion && (command.TargetState == statemachine.ToolCallSucceeded || command.TargetState == statemachine.ToolCallFailed) && command.ResultHash != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ToolCompletedEvent) && validPointer(command.AttemptCompletedEvent) && validPointer(command.GroupJoinedEvent) && validPointer(command.RunResumeQueuedEvent) && validPointer(command.ResumeCommand) && (command.ResumeQueueClass == "interactive" || command.ResumeQueueClass == "background") && command.ResumeResourceClass != "" && command.ResumePriority >= 0 && command.ResumePriority <= 1000 && command.ResumeCostUnits > 0 && command.ResumeCostUnits <= 1_000_000_000_000 && command.ResumeMaxAttempts > 0 && command.ResumeMaxAttempts <= 100
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
