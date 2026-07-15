package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

const maximumChildGroupCancellationBatch = 10

type CancelChildGroupRemainderCommand struct {
	CancellationID, TenantID, StoreEpoch string
	ExpectedVersion                      uint64
	ExpectedCursor                       string
	BatchSize                            int
	Actor                                json.RawMessage
	CorrelationID                        string
	Children                             map[string]PropagatedRunCancellationPayloads
}

type CancelledChildGroupRemainder struct {
	CancellationID, GroupID, ParentRunID, RootRunID string
	Version                                         uint64
	PreviousCursor, NextCursor                      string
	ScannedChildren, RequestedChildren              int
	AlreadyConverging, TerminalChildren             int
	Complete, Replayed                              bool
}

type childGroupCancellationChild struct {
	id, status                   string
	activeCommand, activeAttempt sql.NullString
}

// CancelChildGroupRemainder advances one bounded page of group members after
// an any/quorum join. Every live sibling receives an independent root
// cancellation barrier; descendants are handled by the regular propagator.
func (store RunStore) CancelChildGroupRemainder(ctx context.Context, command CancelChildGroupRemainderCommand) (CancelledChildGroupRemainder, error) {
	if !store.validClaim() || !validCancelChildGroupRemainder(command) {
		return CancelledChildGroupRemainder{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, command.StoreEpoch); err != nil {
		return CancelledChildGroupRemainder{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CancelledChildGroupRemainder{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return CancelledChildGroupRemainder{}, err
	}

	var groupID, parentRunID, rootRunID, status string
	var version uint64
	var cursor sql.NullString
	err = tx.QueryRow(ctx, `SELECT group_id::text,parent_run_id::text,root_run_id::text,status,version,cursor::text FROM agent.child_group_cancellations WHERE tenant_id=$1 AND id=$2 AND store_epoch=$3 FOR UPDATE`, command.TenantID, command.CancellationID, command.StoreEpoch).Scan(&groupID, &parentRunID, &rootRunID, &status, &version, &cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return CancelledChildGroupRemainder{}, ErrRunConflict
	}
	if err != nil {
		return CancelledChildGroupRemainder{}, err
	}
	previousCursor := ""
	if cursor.Valid {
		previousCursor = cursor.String
	}
	result := CancelledChildGroupRemainder{CancellationID: command.CancellationID, GroupID: groupID, ParentRunID: parentRunID, RootRunID: rootRunID, Version: version, PreviousCursor: previousCursor, NextCursor: previousCursor, Complete: status == "completed"}
	if status == "completed" {
		result.Replayed = true
		if err = tx.Commit(ctx); err != nil {
			return CancelledChildGroupRemainder{}, err
		}
		return result, nil
	}
	if status != "pending" || version != command.ExpectedVersion || previousCursor != command.ExpectedCursor {
		return CancelledChildGroupRemainder{}, ErrRunConflict
	}

	var parentUserID, joinedEventID, joinPolicy string
	var joined bool
	err = tx.QueryRow(ctx, `SELECT p.user_id::text,g.joined_event_id::text,g.join_policy,g.joined FROM agent.child_groups g JOIN agent.runs p ON p.tenant_id=g.tenant_id AND p.id=g.parent_run_id WHERE g.tenant_id=$1 AND g.id=$2 AND g.parent_run_id=$3 AND p.root_run_id=$4`, command.TenantID, groupID, parentRunID, rootRunID).Scan(&parentUserID, &joinedEventID, &joinPolicy, &joined)
	if err != nil || !joined || (joinPolicy != "any" && joinPolicy != "quorum") {
		return CancelledChildGroupRemainder{}, ErrRunConflict
	}

	rows, err := tx.Query(ctx, `SELECT r.id::text,r.status,r.active_command_id::text,r.active_attempt_id::text FROM agent.child_group_members m JOIN agent.runs r ON r.tenant_id=m.tenant_id AND r.id=m.child_run_id WHERE m.tenant_id=$1 AND m.group_id=$2 AND (NULLIF($3,'')::uuid IS NULL OR r.id>NULLIF($3,'')::uuid) ORDER BY r.id LIMIT $4`, command.TenantID, groupID, previousCursor, command.BatchSize+1)
	if err != nil {
		return CancelledChildGroupRemainder{}, err
	}
	children := make([]childGroupCancellationChild, 0, command.BatchSize+1)
	for rows.Next() {
		var child childGroupCancellationChild
		if err = rows.Scan(&child.id, &child.status, &child.activeCommand, &child.activeAttempt); err != nil {
			rows.Close()
			return CancelledChildGroupRemainder{}, err
		}
		children = append(children, child)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return CancelledChildGroupRemainder{}, err
	}
	rows.Close()
	hasMore := len(children) > command.BatchSize
	if hasMore {
		children = children[:command.BatchSize]
	}

	for _, observed := range children {
		if observed.activeCommand.Valid != observed.activeAttempt.Valid {
			return CancelledChildGroupRemainder{}, ErrRunConflict
		}
		if observed.activeCommand.Valid {
			var inboxID string
			if err = tx.QueryRow(ctx, `SELECT id::text FROM agent.inbox WHERE tenant_id=$1 AND command_id=$2 AND owner_attempt_id=$3 AND status='running' FOR UPDATE`, command.TenantID, observed.activeCommand.String, observed.activeAttempt.String).Scan(&inboxID); err != nil {
				return CancelledChildGroupRemainder{}, ErrRunConflict
			}
		}
		var childStatus, childUserID, childRootRunID string
		var childVersion, cancelGeneration, currentFence uint64
		var activeCancellation, activeCommand, activeAttempt sql.NullString
		err = tx.QueryRow(ctx, `SELECT r.status,r.user_id::text,r.root_run_id::text,r.run_version,r.cancel_generation,r.current_fence,r.active_cancellation_id::text,r.active_command_id::text,r.active_attempt_id::text FROM agent.runs r JOIN agent.child_group_members m ON m.tenant_id=r.tenant_id AND m.child_run_id=r.id AND m.group_id=$3 WHERE r.tenant_id=$1 AND r.id=$2 FOR UPDATE OF r`, command.TenantID, observed.id, groupID).Scan(&childStatus, &childUserID, &childRootRunID, &childVersion, &cancelGeneration, &currentFence, &activeCancellation, &activeCommand, &activeAttempt)
		if err != nil || childRootRunID != rootRunID || activeCommand.Valid != observed.activeCommand.Valid || activeAttempt.Valid != observed.activeAttempt.Valid || activeCommand.Valid && (activeCommand.String != observed.activeCommand.String || activeAttempt.String != observed.activeAttempt.String) {
			return CancelledChildGroupRemainder{}, ErrRunConflict
		}
		result.ScannedChildren++
		result.NextCursor = observed.id
		if statemachine.Runs.IsTerminal(statemachine.RunState(childStatus)) {
			result.TerminalChildren++
			continue
		}
		if activeCancellation.Valid {
			result.AlreadyConverging++
			continue
		}
		payloads, ok := command.Children[observed.id]
		if !ok || !validPropagatedRunCancellationPayloads(payloads) || cancelGeneration == ^uint64(0) {
			return CancelledChildGroupRemainder{}, ErrInvalidCommand
		}
		childCancellationID, identifierErr := store.childGroupRemainderRunCancellationID(groupID, observed.id)
		if identifierErr != nil {
			return CancelledChildGroupRemainder{}, identifierErr
		}
		identifiers, identifierErr := store.cancellationIdentifiers(childCancellationID)
		if identifierErr != nil {
			return CancelledChildGroupRemainder{}, identifierErr
		}
		propagation, identifierErr := store.cancellationPropagationIdentifiers(childCancellationID)
		if identifierErr != nil {
			return CancelledChildGroupRemainder{}, identifierErr
		}
		var descendants int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2 AND status NOT IN ('succeeded','failed','cancelled','expired')`, command.TenantID, observed.id).Scan(&descendants); err != nil {
			return CancelledChildGroupRemainder{}, err
		}
		needsPropagation := descendants > 0
		nextGeneration := cancelGeneration + 1
		reconcileAt := now.Add(cancellationReconciliationDelay).UTC().Truncate(time.Microsecond)
		requestHash := fmt.Sprintf("%x", sha256.Sum256([]byte(groupID+"\x00"+observed.id+fmt.Sprintf("\x00%d", nextGeneration))))
		if _, err = tx.Exec(ctx, `INSERT INTO agent.run_cancellations(id,tenant_id,run_id,root_cancellation_id,parent_cancellation_id,cancel_generation,status,requested_by,requested_at,reason,store_epoch,request_hash,request_event_id,request_payload_ref,request_payload_hash,settlement_payload_ref,settlement_payload_hash,attempt_cancelled_payload_ref,attempt_cancelled_payload_hash,reconciliation_due_at,propagation_complete,propagation_command_id,propagation_payload_ref,propagation_payload_hash,created_at,updated_at) VALUES($1,$2,$3,$1,NULL,$4,'requested',$5,$6,'child_group_policy_satisfied',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,NULLIF($18,'')::uuid,NULLIF($19,''),NULLIF($20,''),$6,$6)`, childCancellationID, command.TenantID, observed.id, nextGeneration, parentUserID, now, command.StoreEpoch, requestHash, identifiers.requestEvent, payloads.RequestEvent.Ref, payloads.RequestEvent.Hash, payloads.SettlementEvent.Ref, payloads.SettlementEvent.Hash, payloads.AttemptCancelledEvent.Ref, payloads.AttemptCancelledEvent.Hash, reconcileAt, !needsPropagation, propagationValue(needsPropagation, propagation.command), propagationValue(needsPropagation, payloads.PropagateCommand.Ref), propagationValue(needsPropagation, payloads.PropagateCommand.Hash)); err != nil {
			return CancelledChildGroupRemainder{}, err
		}
		commands := []eventpostgres.OutboxCommand{{ID: identifiers.requestOutbox, CommandID: identifiers.requestPublish, CommandType: "events.publish", PayloadRef: payloads.RequestEvent.Ref, PayloadHash: payloads.RequestEvent.Hash}, {ID: identifiers.reconcileOutbox, CommandID: identifiers.reconcileCommand, CommandType: "ReconcileRunCancellation", PayloadRef: payloads.ReconcileCommand.Ref, PayloadHash: payloads.ReconcileCommand.Hash}}
		if needsPropagation {
			commands = append(commands, eventpostgres.OutboxCommand{ID: propagation.outbox, CommandID: propagation.command, CommandType: "PropagateRunCancellation", PayloadRef: payloads.PropagateCommand.Ref, PayloadHash: payloads.PropagateCommand.Hash})
		}
		causationID := joinedEventID
		request := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.requestEvent, TenantID: command.TenantID, UserID: childUserID, EventType: "RunCancellationRequested", SchemaVersion: 1, AggregateKind: "run_cancellation", AggregateID: childCancellationID, AggregateVersion: 1, StoreEpoch: command.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: payloads.RequestEvent.Ref, PayloadHash: payloads.RequestEvent.Hash}, Commands: commands}
		if _, err = store.Appender.Append(ctx, tx, request); err != nil {
			return CancelledChildGroupRemainder{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,'background','run-cancellation',90,1,100,'pending',$4,$5,$4,$6,$6)`, identifiers.reconcileJob, command.TenantID, identifiers.reconcileCommand, reconcileAt, reconcileAt.Add(24*time.Hour), now); err != nil {
			return CancelledChildGroupRemainder{}, err
		}
		if needsPropagation {
			if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,'background','run-cancellation-propagation',95,1,100,'pending',$4,$5,$4,$6,$6)`, propagation.job, command.TenantID, propagation.command, reconcileAt, reconcileAt.Add(24*time.Hour), now); err != nil {
				return CancelledChildGroupRemainder{}, err
			}
		}
		nextFence := currentFence
		if childStatus == string(statemachine.RunExecuting) {
			nextFence++
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET current_fence=$1,cancel_requested_at=$2,cancel_generation=$3,active_cancellation_id=$4,updated_at=$2 WHERE tenant_id=$5 AND id=$6 AND run_version=$7 AND cancel_requested_at IS NULL`, nextFence, now, nextGeneration, childCancellationID, command.TenantID, observed.id, childVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return CancelledChildGroupRemainder{}, ErrRunConflict
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=2,status='terminating',updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=1 AND status='requested'`, now, command.TenantID, childCancellationID); updateErr != nil || tag.RowsAffected() != 1 {
			return CancelledChildGroupRemainder{}, ErrRunConflict
		}
		result.RequestedChildren++
	}

	result.Complete = !hasMore
	statusUpdate := "pending"
	var completedAt any
	if result.Complete {
		statusUpdate, completedAt = "completed", now
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.child_group_cancellations SET version=version+1,status=$1,cursor=$2,completed_at=$3,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status='pending' AND cursor IS NOT DISTINCT FROM NULLIF($8,'')::uuid`, statusUpdate, nullableString(result.NextCursor), completedAt, now, command.TenantID, command.CancellationID, version, command.ExpectedCursor); updateErr != nil || tag.RowsAffected() != 1 {
		return CancelledChildGroupRemainder{}, ErrRunConflict
	}
	if result.Complete {
		workIDs, identifierErr := store.childRemainderCancellationIdentifiers(groupID)
		if identifierErr != nil {
			return CancelledChildGroupRemainder{}, identifierErr
		}
		if _, err = tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, workIDs.command); err != nil {
			return CancelledChildGroupRemainder{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return CancelledChildGroupRemainder{}, err
	}
	result.Version++
	return result, nil
}

func validCancelChildGroupRemainder(command CancelChildGroupRemainderCommand) bool {
	return command.CancellationID != "" && command.TenantID != "" && command.StoreEpoch != "" && command.ExpectedVersion > 0 && command.BatchSize >= 1 && command.BatchSize <= maximumChildGroupCancellationBatch && validJSONObject(command.Actor) && command.CorrelationID != "" && command.Children != nil
}

func (store RunStore) childGroupRemainderRunCancellationID(groupID, childRunID string) (string, error) {
	return ids.DeterministicUUID(store.IDKey, "child-group-remainder-run-cancellation", groupID+"\x00"+childRunID)
}
