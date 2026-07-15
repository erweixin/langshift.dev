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

type ChildRunCompletion struct {
	ResultSummary                         PayloadPointer
	CompletedEvent, GroupJoinedEvent      PayloadPointer
	RunResumeQueuedEvent, ResumeCommand   PayloadPointer
	CancelRemainingCommand                PayloadPointer
	ResumeQueueClass, ResumeResourceClass string
	ResumePriority, ResumeMaxAttempts     int
	ResumeCostUnits                       int64
}

type childJoinInput struct {
	TenantID, UserID, ChildRunID, ParentRunID, RootRunID, GroupID string
	ChildEventID                                                  string
	InheritedBudgetMicrounits                                     int64
	Actor                                                         json.RawMessage
	CorrelationID                                                 string
	Completion                                                    ChildRunCompletion
	Now                                                           time.Time
}

type childJoinResult struct {
	ParentRunVersion                                        uint64
	ContinuationID, ResumeCommandID                         string
	RemainderCancellationID, RemainderCancellationCommandID string
	Resumed                                                 bool
	GroupEvent, ParentEvent                                 *eventpostgres.Input
}

type childRemainderCancellationIDs struct{ work, outbox, command, job string }

// joinCompletedChild releases the child's orchestration reservation and, when
// the locked group policy has converged, creates exactly one durable parent
// continuation. It runs inside the child's terminal transaction so a result
// can never become visible without either a durable join decision or a safe
// no-op for a group that is not ready, cancelled, expired, or already joined.
func (store RunStore) joinCompletedChild(ctx context.Context, tx pgx.Tx, input childJoinInput) (childJoinResult, error) {
	if tag, err := tx.Exec(ctx, `UPDATE agent.orchestration_quotas SET version=version+1,concurrent_children=concurrent_children-1,allocated_budget_microunits=allocated_budget_microunits-$1,updated_at=$2 WHERE tenant_id=$3 AND root_run_id=$4 AND concurrent_children>0 AND allocated_budget_microunits>=$1`, input.InheritedBudgetMicrounits, input.Now, input.TenantID, input.RootRunID); err != nil || tag.RowsAffected() != 1 {
		return childJoinResult{}, ErrRunConflict
	}

	var joinPolicy, continuationKind string
	var requiredCount, quorumCount int
	var groupVersion uint64
	var joined bool
	err := tx.QueryRow(ctx, `SELECT version,join_policy,required_count,quorum_count,continuation_kind,joined FROM agent.child_groups WHERE id=$1 AND tenant_id=$2 AND parent_run_id=$3 FOR UPDATE`, input.GroupID, input.TenantID, input.ParentRunID).Scan(&groupVersion, &joinPolicy, &requiredCount, &quorumCount, &continuationKind, &joined)
	if err != nil || continuationKind != "resume_parent" {
		return childJoinResult{}, ErrRunConflict
	}
	var terminalCount, successCount, liveCount int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE m.required AND r.status IN ('succeeded','failed','cancelled','expired')),count(*) FILTER (WHERE m.required AND r.status='succeeded'),count(*) FILTER (WHERE r.status NOT IN ('succeeded','failed','cancelled','expired')) FROM agent.child_group_members m JOIN agent.runs r ON r.tenant_id=m.tenant_id AND r.id=m.child_run_id WHERE m.tenant_id=$1 AND m.group_id=$2`, input.TenantID, input.GroupID).Scan(&terminalCount, &successCount, &liveCount)
	if err != nil {
		return childJoinResult{}, err
	}
	if joined || !parallelJoinSatisfied(joinPolicy, requiredCount, quorumCount, terminalCount, successCount) {
		return childJoinResult{}, nil
	}
	needsRemainderCancellation := liveCount > 0 && (joinPolicy == "any" || joinPolicy == "quorum")
	if needsRemainderCancellation && !validSHA256Pointer(input.Completion.CancelRemainingCommand) {
		return childJoinResult{}, ErrInvalidCommand
	}

	var parentVersion uint64
	var parentStatus string
	var cancelRequested *time.Time
	var dueAt time.Time
	err = tx.QueryRow(ctx, `SELECT run_version,status,cancel_requested_at,due_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND root_run_id=$3 FOR UPDATE`, input.ParentRunID, input.TenantID, input.RootRunID).Scan(&parentVersion, &parentStatus, &cancelRequested, &dueAt)
	if err != nil {
		return childJoinResult{}, err
	}
	if parentStatus != string(statemachine.RunWaitingChild) || cancelRequested != nil || !dueAt.After(input.Now) {
		return childJoinResult{}, nil
	}
	continuation, err := store.childContinuationIdentifiers(input.GroupID)
	if err != nil {
		return childJoinResult{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.continuations(id,tenant_id,run_id,run_version,group_kind,group_id,command_id,status,continuation_kind,created_at,updated_at) VALUES($1,$2,$3,$4,'child',$5,$6,'preparing','resume_parent',$7,$7) ON CONFLICT DO NOTHING`, continuation.continuation, input.TenantID, input.ParentRunID, parentVersion, input.GroupID, continuation.command, input.Now)
	if err != nil {
		return childJoinResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return childJoinResult{}, ErrRunConflict
	}
	nextParentVersion := parentVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='queued',run_version=$1,pending_command_id=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND status='waiting_child' AND run_version=$6 AND cancel_requested_at IS NULL AND due_at>$3`, nextParentVersion, continuation.command, input.Now, input.ParentRunID, input.TenantID, parentVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return childJoinResult{}, ErrRunConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.continuations SET version=version+1,status='committed',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND status='preparing'`, input.Now, continuation.continuation, input.TenantID); updateErr != nil || tag.RowsAffected() != 1 {
		return childJoinResult{}, ErrRunConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.child_groups SET version=version+1,joined=true,continuation_id=$1,joined_event_id=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND joined=false AND continuation_id IS NULL AND joined_event_id IS NULL`, continuation.continuation, continuation.groupEvent, input.Now, input.GroupID, input.TenantID, groupVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return childJoinResult{}, ErrRunConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9)`, continuation.job, input.TenantID, continuation.command, input.Completion.ResumeQueueClass, input.Completion.ResumeResourceClass, input.Completion.ResumePriority, input.Completion.ResumeCostUnits, input.Completion.ResumeMaxAttempts, input.Now, dueAt); err != nil {
		return childJoinResult{}, err
	}
	var remainder childRemainderCancellationIDs
	if needsRemainderCancellation {
		remainder, err = store.childRemainderCancellationIdentifiers(input.GroupID)
		if err != nil {
			return childJoinResult{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.child_group_cancellations(tenant_id,id,group_id,parent_run_id,root_run_id,store_epoch,status,version,command_id,payload_ref,payload_hash,available_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'pending',1,$7,$8,$9,$10,$10,$10)`, input.TenantID, remainder.work, input.GroupID, input.ParentRunID, input.RootRunID, store.StoreEpoch, remainder.command, input.Completion.CancelRemainingCommand.Ref, input.Completion.CancelRemainingCommand.Hash, input.Now); err != nil {
			return childJoinResult{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,'background','child-group-cancellation',95,1,100,'pending',$4,$5,$4,$4,$4)`, remainder.job, input.TenantID, remainder.command, input.Now, dueAt); err != nil {
			return childJoinResult{}, err
		}
	}

	childCausationID := input.ChildEventID
	groupEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: continuation.groupEvent, TenantID: input.TenantID, UserID: input.UserID, EventType: "ChildGroupJoined", SchemaVersion: 1, AggregateKind: "child_group", AggregateID: input.GroupID, AggregateVersion: groupVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: input.Now, Actor: input.Actor, CausationID: &childCausationID, CorrelationID: input.CorrelationID, PayloadRef: input.Completion.GroupJoinedEvent.Ref, PayloadHash: input.Completion.GroupJoinedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: continuation.groupOutbox, CommandID: continuation.groupPublish, CommandType: "events.publish", PayloadRef: input.Completion.GroupJoinedEvent.Ref, PayloadHash: input.Completion.GroupJoinedEvent.Hash}}}
	groupCausationID := continuation.groupEvent
	parentEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: continuation.runEvent, TenantID: input.TenantID, UserID: input.UserID, EventType: "RunResumeQueued", SchemaVersion: 1, AggregateKind: "run", AggregateID: input.ParentRunID, AggregateVersion: nextParentVersion, StoreEpoch: store.StoreEpoch, OccurredAt: input.Now, Actor: input.Actor, CausationID: &groupCausationID, CorrelationID: input.CorrelationID, PayloadRef: input.Completion.RunResumeQueuedEvent.Ref, PayloadHash: input.Completion.RunResumeQueuedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: continuation.runOutbox, CommandID: continuation.runPublish, CommandType: "events.publish", PayloadRef: input.Completion.RunResumeQueuedEvent.Ref, PayloadHash: input.Completion.RunResumeQueuedEvent.Hash}, {ID: continuation.resumeOutbox, CommandID: continuation.command, CommandType: "ResumeParentRun", PayloadRef: input.Completion.ResumeCommand.Ref, PayloadHash: input.Completion.ResumeCommand.Hash}}}
	if needsRemainderCancellation {
		parentEvent.Commands = append(parentEvent.Commands, eventpostgres.OutboxCommand{ID: remainder.outbox, CommandID: remainder.command, CommandType: "CancelRemainingChildRuns", PayloadRef: input.Completion.CancelRemainingCommand.Ref, PayloadHash: input.Completion.CancelRemainingCommand.Hash})
	}
	return childJoinResult{ParentRunVersion: nextParentVersion, ContinuationID: continuation.continuation, ResumeCommandID: continuation.command, RemainderCancellationID: remainder.work, RemainderCancellationCommandID: remainder.command, Resumed: true, GroupEvent: &groupEvent, ParentEvent: &parentEvent}, nil
}

func validChildRunCompletion(completion ChildRunCompletion) bool {
	return validSHA256Pointer(completion.ResultSummary) && validPointer(completion.CompletedEvent) && validPointer(completion.GroupJoinedEvent) && validPointer(completion.RunResumeQueuedEvent) && validPointer(completion.ResumeCommand) && validSHA256Pointer(completion.CancelRemainingCommand) && (completion.ResumeQueueClass == "interactive" || completion.ResumeQueueClass == "background") && completion.ResumeResourceClass != "" && completion.ResumePriority >= 0 && completion.ResumePriority <= 1000 && completion.ResumeCostUnits > 0 && completion.ResumeCostUnits <= 1_000_000_000_000 && completion.ResumeMaxAttempts > 0 && completion.ResumeMaxAttempts <= 100
}

func (store RunStore) childContinuationIdentifiers(groupID string) (continuationIDs, error) {
	domains := []string{"child-continuation", "resume-parent-command", "resume-parent-job", "child-group-joined-event", "child-group-joined-publish-outbox", "child-group-joined-publish-command", "parent-run-resume-queued-event", "parent-run-resume-queued-publish-outbox", "parent-run-resume-queued-publish-command", "resume-parent-outbox"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, fmt.Sprintf("%s\x00resume-parent", groupID))
		if err != nil {
			return continuationIDs{}, err
		}
		values[index] = value
	}
	return continuationIDs{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8], values[9]}, nil
}

func (store RunStore) childRemainderCancellationIdentifiers(groupID string) (childRemainderCancellationIDs, error) {
	values := make([]string, 4)
	for index, domain := range []string{"child-group-remainder-cancellation", "child-group-remainder-cancellation-outbox", "child-group-remainder-cancellation-command", "child-group-remainder-cancellation-job"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, groupID)
		if err != nil {
			return childRemainderCancellationIDs{}, err
		}
		values[index] = value
	}
	return childRemainderCancellationIDs{values[0], values[1], values[2], values[3]}, nil
}
