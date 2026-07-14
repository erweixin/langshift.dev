package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

type toolJoinInput struct {
	TenantID, UserID, RunID, GroupID, ToolEventID string
	Actor                                         json.RawMessage
	CorrelationID                                 string
	GroupJoinedEvent, RunResumeQueuedEvent        PayloadPointer
	ResumeCommand                                 PayloadPointer
	ResumeQueueClass, ResumeResourceClass         string
	ResumePriority                                int
	ResumeCostUnits                               int64
	ResumeMaxAttempts                             int
	Now                                           time.Time
}

type toolJoinResult struct {
	RunVersion                      uint64
	ContinuationID, ResumeCommandID string
	Resumed                         bool
	GroupEvent, RunEvent            *eventpostgres.Input
}

func (store RunStore) joinCompletedTool(ctx context.Context, tx pgx.Tx, input toolJoinInput) (toolJoinResult, error) {
	var joinPolicy, groupKind, continuationKind string
	var requiredCount, quorumCount int
	var groupVersion uint64
	var joined bool
	err := tx.QueryRow(ctx, `SELECT version,join_policy,required_count,quorum_count,group_kind,continuation_kind,joined FROM agent.parallel_groups WHERE id=$1 AND tenant_id=$2 AND run_id=$3 FOR UPDATE`, input.GroupID, input.TenantID, input.RunID).Scan(&groupVersion, &joinPolicy, &requiredCount, &quorumCount, &groupKind, &continuationKind, &joined)
	if err != nil || groupKind != "execution" || continuationKind != "resume" {
		return toolJoinResult{}, ErrExecutionRightConflict
	}
	var terminalCount, successCount int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE m.required AND t.status IN ('succeeded','failed','cancelled','resolved_unknown')),count(*) FILTER (WHERE m.required AND t.status='succeeded') FROM agent.parallel_group_members m JOIN agent.tool_calls t ON t.tenant_id=m.tenant_id AND t.id=m.tool_call_id WHERE m.tenant_id=$1 AND m.group_id=$2`, input.TenantID, input.GroupID).Scan(&terminalCount, &successCount)
	if err != nil {
		return toolJoinResult{}, err
	}
	if !parallelJoinSatisfied(joinPolicy, requiredCount, quorumCount, terminalCount, successCount) || joined {
		return toolJoinResult{}, nil
	}
	var runVersion uint64
	var runStatus string
	var cancelRequested *time.Time
	var dueAt time.Time
	err = tx.QueryRow(ctx, `SELECT run_version,status,cancel_requested_at,due_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, input.RunID, input.TenantID).Scan(&runVersion, &runStatus, &cancelRequested, &dueAt)
	if err != nil {
		return toolJoinResult{}, err
	}
	if runStatus != string(statemachine.RunWaitingTool) || cancelRequested != nil || !dueAt.After(input.Now) {
		return toolJoinResult{}, nil
	}
	continuation, err := store.continuationIdentifiers(input.GroupID)
	if err != nil {
		return toolJoinResult{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.continuations(id,tenant_id,run_id,run_version,group_kind,group_id,command_id,status,continuation_kind,created_at,updated_at) VALUES($1,$2,$3,$4,'parallel',$5,$6,'preparing','resume',$7,$7) ON CONFLICT DO NOTHING`, continuation.continuation, input.TenantID, input.RunID, runVersion, input.GroupID, continuation.command, input.Now)
	if err != nil {
		return toolJoinResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return toolJoinResult{}, ErrExecutionRightConflict
	}
	nextRunVersion := runVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='queued',run_version=$1,pending_command_id=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND status='waiting_tool' AND run_version=$6 AND cancel_requested_at IS NULL AND due_at>$3`, nextRunVersion, continuation.command, input.Now, input.RunID, input.TenantID, runVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return toolJoinResult{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.continuations SET version=version+1,status='committed',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND status='preparing'`, input.Now, continuation.continuation, input.TenantID); updateErr != nil || tag.RowsAffected() != 1 {
		return toolJoinResult{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.parallel_groups SET version=version+1,joined=true,continuation_id=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND joined=false AND continuation_id IS NULL`, continuation.continuation, input.Now, input.GroupID, input.TenantID, groupVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return toolJoinResult{}, ErrExecutionRightConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9)`, continuation.job, input.TenantID, continuation.command, input.ResumeQueueClass, input.ResumeResourceClass, input.ResumePriority, input.ResumeCostUnits, input.ResumeMaxAttempts, input.Now, dueAt); err != nil {
		return toolJoinResult{}, err
	}
	causationID := input.ToolEventID
	group := eventpostgres.Input{Event: eventpostgres.Event{ID: continuation.groupEvent, TenantID: input.TenantID, UserID: input.UserID, EventType: "ToolGroupJoined", SchemaVersion: 1, AggregateKind: "parallel_group", AggregateID: input.GroupID, AggregateVersion: groupVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: input.Now, Actor: input.Actor, CausationID: &causationID, CorrelationID: input.CorrelationID, PayloadRef: input.GroupJoinedEvent.Ref, PayloadHash: input.GroupJoinedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: continuation.groupOutbox, CommandID: continuation.groupPublish, CommandType: "events.publish", PayloadRef: input.GroupJoinedEvent.Ref, PayloadHash: input.GroupJoinedEvent.Hash}}}
	groupCausationID := continuation.groupEvent
	resume := eventpostgres.Input{Event: eventpostgres.Event{ID: continuation.runEvent, TenantID: input.TenantID, UserID: input.UserID, EventType: "RunResumeQueued", SchemaVersion: 1, AggregateKind: "run", AggregateID: input.RunID, AggregateVersion: nextRunVersion, StoreEpoch: store.StoreEpoch, OccurredAt: input.Now, Actor: input.Actor, CausationID: &groupCausationID, CorrelationID: input.CorrelationID, PayloadRef: input.RunResumeQueuedEvent.Ref, PayloadHash: input.RunResumeQueuedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: continuation.runOutbox, CommandID: continuation.runPublish, CommandType: "events.publish", PayloadRef: input.RunResumeQueuedEvent.Ref, PayloadHash: input.RunResumeQueuedEvent.Hash}, {ID: continuation.resumeOutbox, CommandID: continuation.command, CommandType: "ResumeAgentRun", PayloadRef: input.ResumeCommand.Ref, PayloadHash: input.ResumeCommand.Hash}}}
	return toolJoinResult{RunVersion: nextRunVersion, ContinuationID: continuation.continuation, ResumeCommandID: continuation.command, Resumed: true, GroupEvent: &group, RunEvent: &resume}, nil
}
