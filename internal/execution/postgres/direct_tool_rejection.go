package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

type RejectDirectApprovalGroupCommand struct {
	TenantID, ApprovalID, ApprovalEventID string
	ExpectedApprovalVersion               uint64
	Actor                                 json.RawMessage
	CorrelationID                         string
	ToolCancelledEvent, RunCancelledEvent PayloadPointer
}

type RejectedDirectApprovalGroup struct {
	RunID, GroupID string
	RunVersion     uint64
	CancelledTools int
	CancelledAt    time.Time
	Replayed       bool
}

type directRejectionIDs struct {
	runEvent, runOutbox, runPublish string
}

type directRejectedToolIDs struct{ event, outbox, publish string }

// RejectDirectApprovalGroup cancels the waiting Run and every not-yet-running
// ToolCall in the direct approval group in one transaction. A tool that has
// already acquired execution rights fails closed here and must use the normal
// cancellation barrier instead of pretending the external effect was stopped.
func (store RunStore) RejectDirectApprovalGroup(ctx context.Context, command RejectDirectApprovalGroupCommand) (RejectedDirectApprovalGroup, error) {
	if !store.valid() || !validRejectDirectApprovalGroup(command) {
		return RejectedDirectApprovalGroup{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return RejectedDirectApprovalGroup{}, err
	}
	identifier, err := store.directRejectionIdentifiers(command.ApprovalID)
	if err != nil {
		return RejectedDirectApprovalGroup{}, ErrConfiguration
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RejectedDirectApprovalGroup{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return RejectedDirectApprovalGroup{}, err
	}
	var approvalStatus, runID, toolID string
	var approvalVersion uint64
	err = tx.QueryRow(ctx, `SELECT status,version,run_id::text,tool_call_id::text FROM agent.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ApprovalID).Scan(&approvalStatus, &approvalVersion, &runID, &toolID)
	if err != nil || approvalStatus != "rejected" || approvalVersion != command.ExpectedApprovalVersion {
		return RejectedDirectApprovalGroup{}, ErrDirectApprovalAuthorization
	}
	var rejectionExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='ApprovalRejected' AND aggregate_kind='approval' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5)`, command.TenantID, command.ApprovalEventID, command.ApprovalID, approvalVersion, store.StoreEpoch).Scan(&rejectionExists); err != nil || !rejectionExists {
		return RejectedDirectApprovalGroup{}, ErrDirectApprovalAuthorization
	}
	var groupID, groupKind, continuationKind string
	var groupVersion uint64
	var joined bool
	err = tx.QueryRow(ctx, `SELECT g.id::text,g.version,g.group_kind,g.continuation_kind,g.joined FROM agent.parallel_group_members m JOIN agent.parallel_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id WHERE m.tenant_id=$1 AND m.tool_call_id=$2 FOR UPDATE OF g`, command.TenantID, toolID).Scan(&groupID, &groupVersion, &groupKind, &continuationKind, &joined)
	if err != nil {
		return RejectedDirectApprovalGroup{}, ErrDirectApprovalAuthorization
	}
	var runStatus, userID string
	var runVersion uint64
	var cancelRequested *time.Time
	err = tx.QueryRow(ctx, `SELECT status,run_version,user_id::text,cancel_requested_at FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, runID).Scan(&runStatus, &runVersion, &userID, &cancelRequested)
	if err != nil {
		return RejectedDirectApprovalGroup{}, err
	}
	if runStatus == string(statemachine.RunCancelled) {
		var runEventExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND aggregate_kind='run' AND aggregate_id=$3 AND aggregate_version=$4 AND event_type='RunCancelled' AND store_epoch=$5)`, command.TenantID, identifier.runEvent, runID, runVersion, store.StoreEpoch).Scan(&runEventExists); err != nil || !runEventExists {
			return RejectedDirectApprovalGroup{}, fmt.Errorf("%w: cancellation replay evidence", ErrDirectApprovalConflict)
		}
		if err = tx.Commit(ctx); err != nil {
			return RejectedDirectApprovalGroup{}, err
		}
		return RejectedDirectApprovalGroup{RunID: runID, GroupID: groupID, RunVersion: runVersion, CancelledAt: now, Replayed: true}, nil
	}
	if runStatus != string(statemachine.RunWaitingApproval) || cancelRequested != nil || joined || groupKind != "approval_direct" || continuationKind != "request_approval" {
		return RejectedDirectApprovalGroup{}, ErrDirectApprovalAuthorization
	}
	if err = statemachine.Runs.ValidateTransition(statemachine.RunWaitingApproval, statemachine.RunCancelled); err != nil {
		return RejectedDirectApprovalGroup{}, ErrInvalidCommand
	}
	type member struct {
		id, status string
		version    uint64
		pending    sql.NullString
	}
	rows, err := tx.Query(ctx, `SELECT t.id::text,t.status,t.tool_call_version,t.pending_command_id::text FROM agent.parallel_group_members m JOIN agent.tool_calls t ON t.tenant_id=m.tenant_id AND t.id=m.tool_call_id WHERE m.tenant_id=$1 AND m.group_id=$2 ORDER BY t.id FOR UPDATE OF t`, command.TenantID, groupID)
	if err != nil {
		return RejectedDirectApprovalGroup{}, err
	}
	members := []member{}
	for rows.Next() {
		var value member
		if err = rows.Scan(&value.id, &value.status, &value.version, &value.pending); err != nil {
			rows.Close()
			return RejectedDirectApprovalGroup{}, err
		}
		members = append(members, value)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(members) == 0 {
		return RejectedDirectApprovalGroup{}, fmt.Errorf("%w: direct group members", ErrDirectApprovalConflict)
	}
	for _, value := range members {
		state := statemachine.ToolCallState(value.status)
		if state != statemachine.ToolCallAwaitingApproval && state != statemachine.ToolCallRequested {
			return RejectedDirectApprovalGroup{}, ErrDirectApprovalAuthorization
		}
		if err = statemachine.ToolCalls.ValidateTransition(state, statemachine.ToolCallCancelled); err != nil {
			return RejectedDirectApprovalGroup{}, ErrInvalidCommand
		}
		if value.status == string(statemachine.ToolCallRequested) {
			if !value.pending.Valid {
				return RejectedDirectApprovalGroup{}, fmt.Errorf("%w: requested tool command", ErrDirectApprovalConflict)
			}
			if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, value.pending.String); updateErr != nil || tag.RowsAffected() != 1 {
				return RejectedDirectApprovalGroup{}, ErrDirectApprovalAuthorization
			}
		}
		toolIDs, idErr := store.directRejectedToolIdentifiers(command.ApprovalID, value.id)
		if idErr != nil {
			return RejectedDirectApprovalGroup{}, idErr
		}
		nextVersion := value.version + 1
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status='cancelled',tool_call_version=$1,pending_command_id=NULL,result_event_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND status=$6 AND tool_call_version=$7`, nextVersion, toolIDs.event, now, command.TenantID, value.id, value.status, value.version); updateErr != nil || tag.RowsAffected() != 1 {
			return RejectedDirectApprovalGroup{}, fmt.Errorf("%w: tool cancellation", ErrDirectApprovalConflict)
		}
		causationID := command.ApprovalEventID
		toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: toolIDs.event, TenantID: command.TenantID, UserID: userID, EventType: "ToolCallCancelled", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: value.id, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.ToolCancelledEvent.Ref, PayloadHash: command.ToolCancelledEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: toolIDs.outbox, CommandID: toolIDs.publish, CommandType: "events.publish", PayloadRef: command.ToolCancelledEvent.Ref, PayloadHash: command.ToolCancelledEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
			return RejectedDirectApprovalGroup{}, err
		}
	}
	nextRunVersion := runVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='cancelled',run_version=$1,pending_command_id=NULL,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status='waiting_approval' AND run_version=$5 AND cancel_requested_at IS NULL`, nextRunVersion, now, command.TenantID, runID, runVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return RejectedDirectApprovalGroup{}, fmt.Errorf("%w: run cancellation", ErrDirectApprovalConflict)
	}
	rejectionCausation := command.ApprovalEventID
	runEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: identifier.runEvent, TenantID: command.TenantID, UserID: userID, EventType: "RunCancelled", SchemaVersion: 1, AggregateKind: "run", AggregateID: runID, AggregateVersion: nextRunVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &rejectionCausation, CorrelationID: command.CorrelationID, PayloadRef: command.RunCancelledEvent.Ref, PayloadHash: command.RunCancelledEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifier.runOutbox, CommandID: identifier.runPublish, CommandType: "events.publish", PayloadRef: command.RunCancelledEvent.Ref, PayloadHash: command.RunCancelledEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, runEvent); err != nil {
		return RejectedDirectApprovalGroup{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RejectedDirectApprovalGroup{}, err
	}
	return RejectedDirectApprovalGroup{RunID: runID, GroupID: groupID, RunVersion: nextRunVersion, CancelledTools: len(members), CancelledAt: now}, nil
}

func (store RunStore) directRejectionIdentifiers(approvalID string) (directRejectionIDs, error) {
	values := make([]string, 3)
	for index, domain := range []string{"run-event", "run-outbox", "run-publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, "direct-approval-reject:"+domain, approvalID)
		if err != nil {
			return directRejectionIDs{}, err
		}
		values[index] = value
	}
	return directRejectionIDs{values[0], values[1], values[2]}, nil
}

func (store RunStore) directRejectedToolIdentifiers(approvalID, toolID string) (directRejectedToolIDs, error) {
	values := make([]string, 3)
	for index, domain := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, "direct-approval-reject:tool-"+domain, approvalID+"\x00"+toolID)
		if err != nil {
			return directRejectedToolIDs{}, err
		}
		values[index] = value
	}
	return directRejectedToolIDs{values[0], values[1], values[2]}, nil
}

func validRejectDirectApprovalGroup(command RejectDirectApprovalGroupCommand) bool {
	return command.TenantID != "" && command.ApprovalID != "" && command.ApprovalEventID != "" && command.ExpectedApprovalVersion > 1 && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ToolCancelledEvent) && validPointer(command.RunCancelledEvent)
}
