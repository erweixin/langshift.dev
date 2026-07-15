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

const maximumCancellationPropagationBatch = 10

type PropagatedRunCancellationPayloads struct {
	RequestEvent, SettlementEvent, AttemptCancelledEvent PayloadPointer
	ReconcileCommand, PropagateCommand                   PayloadPointer
}

type PropagateRunCancellationCommand struct {
	CancellationID, TenantID, StoreEpoch string
	ExpectedCancellationVersion          uint64
	ExpectedCursor                       string
	BatchSize                            int
	Actor                                json.RawMessage
	CorrelationID                        string
	Children                             map[string]PropagatedRunCancellationPayloads
}

type PropagatedRunCancellation struct {
	CancellationID, RunID, RootCancellationID string
	CancellationVersion                       uint64
	PreviousCursor, NextCursor                string
	ScannedChildren, RequestedChildren        int
	AlreadyConverging, TerminalChildren       int
	Complete, Replayed                        bool
}

type propagationChild struct {
	id, status                   string
	activeCommand, activeAttempt sql.NullString
}

// PropagateRunCancellation advances one stable, bounded page of direct child
// Runs. The parent cancellation row serializes cursor movement; each live child
// receives an immutable lineage-bound local barrier and its own reconciliation
// (and, when needed, next-level propagation) command.
func (store RunStore) PropagateRunCancellation(ctx context.Context, command PropagateRunCancellationCommand) (PropagatedRunCancellation, error) {
	if !store.validClaim() || !validPropagateRunCancellation(command) {
		return PropagatedRunCancellation{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, command.StoreEpoch); err != nil {
		return PropagatedRunCancellation{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PropagatedRunCancellation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return PropagatedRunCancellation{}, err
	}

	var runID, rootCancellationID, status, requestedBy, reason, requestEventID string
	var version uint64
	var cursor sql.NullString
	var complete bool
	err = tx.QueryRow(ctx, `SELECT run_id::text,root_cancellation_id::text,status,version,propagation_cursor::text,propagation_complete,requested_by::text,reason,request_event_id::text FROM agent.run_cancellations WHERE tenant_id=$1 AND id=$2 AND store_epoch=$3 FOR UPDATE`, command.TenantID, command.CancellationID, command.StoreEpoch).Scan(&runID, &rootCancellationID, &status, &version, &cursor, &complete, &requestedBy, &reason, &requestEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return PropagatedRunCancellation{}, ErrRunConflict
	}
	if err != nil {
		return PropagatedRunCancellation{}, err
	}
	previousCursor := ""
	if cursor.Valid {
		previousCursor = cursor.String
	}
	result := PropagatedRunCancellation{CancellationID: command.CancellationID, RunID: runID, RootCancellationID: rootCancellationID, CancellationVersion: version, PreviousCursor: previousCursor, NextCursor: previousCursor, Complete: complete}
	if complete {
		result.Replayed = true
		if err = tx.Commit(ctx); err != nil {
			return PropagatedRunCancellation{}, err
		}
		return result, nil
	}
	if status != "terminating" || version != command.ExpectedCancellationVersion || previousCursor != command.ExpectedCursor {
		return PropagatedRunCancellation{}, ErrRunConflict
	}

	rows, err := tx.Query(ctx, `SELECT id::text,status,active_command_id::text,active_attempt_id::text FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2 AND (NULLIF($3,'')::uuid IS NULL OR id>NULLIF($3,'')::uuid) ORDER BY id LIMIT $4`, command.TenantID, runID, previousCursor, command.BatchSize+1)
	if err != nil {
		return PropagatedRunCancellation{}, err
	}
	children := make([]propagationChild, 0, command.BatchSize+1)
	for rows.Next() {
		var child propagationChild
		if err = rows.Scan(&child.id, &child.status, &child.activeCommand, &child.activeAttempt); err != nil {
			rows.Close()
			return PropagatedRunCancellation{}, err
		}
		children = append(children, child)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return PropagatedRunCancellation{}, err
	}
	rows.Close()
	hasMore := len(children) > command.BatchSize
	if hasMore {
		children = children[:command.BatchSize]
	}

	for _, observed := range children {
		if observed.activeCommand.Valid != observed.activeAttempt.Valid {
			return PropagatedRunCancellation{}, ErrRunConflict
		}
		if observed.activeCommand.Valid {
			var inboxID string
			if err = tx.QueryRow(ctx, `SELECT id::text FROM agent.inbox WHERE tenant_id=$1 AND command_id=$2 AND owner_attempt_id=$3 AND status='running' FOR UPDATE`, command.TenantID, observed.activeCommand.String, observed.activeAttempt.String).Scan(&inboxID); err != nil {
				return PropagatedRunCancellation{}, ErrRunConflict
			}
		}
		var childStatus, childUserID, childRootRunID string
		var childVersion, cancelGeneration, currentFence uint64
		var activeCancellation, activeCommand, activeAttempt sql.NullString
		err = tx.QueryRow(ctx, `SELECT status,user_id::text,root_run_id::text,run_version,cancel_generation,current_fence,active_cancellation_id::text,active_command_id::text,active_attempt_id::text FROM agent.runs WHERE tenant_id=$1 AND id=$2 AND parent_run_id=$3 FOR UPDATE`, command.TenantID, observed.id, runID).Scan(&childStatus, &childUserID, &childRootRunID, &childVersion, &cancelGeneration, &currentFence, &activeCancellation, &activeCommand, &activeAttempt)
		if err != nil || activeCommand.Valid != observed.activeCommand.Valid || activeAttempt.Valid != observed.activeAttempt.Valid || activeCommand.Valid && (activeCommand.String != observed.activeCommand.String || activeAttempt.String != observed.activeAttempt.String) {
			return PropagatedRunCancellation{}, ErrRunConflict
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
			return PropagatedRunCancellation{}, ErrInvalidCommand
		}
		childCancellationID, identifierErr := store.propagatedCancellationID(rootCancellationID, observed.id)
		if identifierErr != nil {
			return PropagatedRunCancellation{}, identifierErr
		}
		identifiers, identifierErr := store.cancellationIdentifiers(childCancellationID)
		if identifierErr != nil {
			return PropagatedRunCancellation{}, identifierErr
		}
		propagation, identifierErr := store.cancellationPropagationIdentifiers(childCancellationID)
		if identifierErr != nil {
			return PropagatedRunCancellation{}, identifierErr
		}
		var descendants int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2 AND status NOT IN ('succeeded','failed','cancelled','expired')`, command.TenantID, observed.id).Scan(&descendants); err != nil {
			return PropagatedRunCancellation{}, err
		}
		needsPropagation := descendants > 0
		nextGeneration := cancelGeneration + 1
		reconcileAt := now.Add(cancellationReconciliationDelay).UTC().Truncate(time.Microsecond)
		requestHash := fmt.Sprintf("%x", sha256.Sum256([]byte(rootCancellationID+"\x00"+command.CancellationID+"\x00"+observed.id+fmt.Sprintf("\x00%d", nextGeneration))))
		if _, err = tx.Exec(ctx, `INSERT INTO agent.run_cancellations(id,tenant_id,run_id,root_cancellation_id,parent_cancellation_id,cancel_generation,status,requested_by,requested_at,reason,store_epoch,request_hash,request_event_id,request_payload_ref,request_payload_hash,settlement_payload_ref,settlement_payload_hash,attempt_cancelled_payload_ref,attempt_cancelled_payload_hash,reconciliation_due_at,propagation_complete,propagation_command_id,propagation_payload_ref,propagation_payload_hash,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'requested',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,NULLIF($21,'')::uuid,NULLIF($22,''),NULLIF($23,''),$8,$8)`, childCancellationID, command.TenantID, observed.id, rootCancellationID, command.CancellationID, nextGeneration, requestedBy, now, reason, command.StoreEpoch, requestHash, identifiers.requestEvent, payloads.RequestEvent.Ref, payloads.RequestEvent.Hash, payloads.SettlementEvent.Ref, payloads.SettlementEvent.Hash, payloads.AttemptCancelledEvent.Ref, payloads.AttemptCancelledEvent.Hash, reconcileAt, !needsPropagation, propagationValue(needsPropagation, propagation.command), propagationValue(needsPropagation, payloads.PropagateCommand.Ref), propagationValue(needsPropagation, payloads.PropagateCommand.Hash)); err != nil {
			return PropagatedRunCancellation{}, err
		}
		commands := []eventpostgres.OutboxCommand{{ID: identifiers.requestOutbox, CommandID: identifiers.requestPublish, CommandType: "events.publish", PayloadRef: payloads.RequestEvent.Ref, PayloadHash: payloads.RequestEvent.Hash}, {ID: identifiers.reconcileOutbox, CommandID: identifiers.reconcileCommand, CommandType: "ReconcileRunCancellation", PayloadRef: payloads.ReconcileCommand.Ref, PayloadHash: payloads.ReconcileCommand.Hash}}
		if needsPropagation {
			commands = append(commands, eventpostgres.OutboxCommand{ID: propagation.outbox, CommandID: propagation.command, CommandType: "PropagateRunCancellation", PayloadRef: payloads.PropagateCommand.Ref, PayloadHash: payloads.PropagateCommand.Hash})
		}
		causationID := requestEventID
		request := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.requestEvent, TenantID: command.TenantID, UserID: childUserID, EventType: "RunCancellationRequested", SchemaVersion: 1, AggregateKind: "run_cancellation", AggregateID: childCancellationID, AggregateVersion: 1, StoreEpoch: command.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: payloads.RequestEvent.Ref, PayloadHash: payloads.RequestEvent.Hash}, Commands: commands}
		if _, err = store.Appender.Append(ctx, tx, request); err != nil {
			return PropagatedRunCancellation{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,'background','run-cancellation',90,1,100,'pending',$4,$5,$4,$6,$6)`, identifiers.reconcileJob, command.TenantID, identifiers.reconcileCommand, reconcileAt, reconcileAt.Add(24*time.Hour), now); err != nil {
			return PropagatedRunCancellation{}, err
		}
		if needsPropagation {
			if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,'background','run-cancellation-propagation',95,1,100,'pending',$4,$5,$4,$6,$6)`, propagation.job, command.TenantID, propagation.command, reconcileAt, reconcileAt.Add(24*time.Hour), now); err != nil {
				return PropagatedRunCancellation{}, err
			}
		}
		nextFence := currentFence
		if childStatus == string(statemachine.RunExecuting) {
			nextFence++
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET current_fence=$1,cancel_requested_at=$2,cancel_generation=$3,active_cancellation_id=$4,updated_at=$2 WHERE tenant_id=$5 AND id=$6 AND run_version=$7 AND cancel_requested_at IS NULL`, nextFence, now, nextGeneration, childCancellationID, command.TenantID, observed.id, childVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return PropagatedRunCancellation{}, ErrRunConflict
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=2,status='terminating',updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=1 AND status='requested'`, now, command.TenantID, childCancellationID); updateErr != nil || tag.RowsAffected() != 1 {
			return PropagatedRunCancellation{}, ErrRunConflict
		}
		if childRootRunID == "" {
			return PropagatedRunCancellation{}, ErrRunConflict
		}
		result.RequestedChildren++
	}

	result.Complete = !hasMore
	nextCursor := nullableString(result.NextCursor)
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=version+1,propagation_cursor=$1,propagation_complete=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status='terminating' AND propagation_complete=false AND propagation_cursor IS NOT DISTINCT FROM NULLIF($7,'')::uuid`, nextCursor, result.Complete, now, command.TenantID, command.CancellationID, version, command.ExpectedCursor); updateErr != nil || tag.RowsAffected() != 1 {
		return PropagatedRunCancellation{}, ErrRunConflict
	}
	if result.Complete {
		parentPropagation, identifierErr := store.cancellationPropagationIdentifiers(command.CancellationID)
		if identifierErr != nil {
			return PropagatedRunCancellation{}, identifierErr
		}
		if _, err = tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, parentPropagation.command); err != nil {
			return PropagatedRunCancellation{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return PropagatedRunCancellation{}, err
	}
	result.CancellationVersion++
	return result, nil
}

func validPropagateRunCancellation(command PropagateRunCancellationCommand) bool {
	return command.CancellationID != "" && command.TenantID != "" && command.StoreEpoch != "" && command.ExpectedCancellationVersion > 0 && command.BatchSize >= 1 && command.BatchSize <= maximumCancellationPropagationBatch && validJSONObject(command.Actor) && command.CorrelationID != "" && command.Children != nil
}

func validPropagatedRunCancellationPayloads(payloads PropagatedRunCancellationPayloads) bool {
	return validSHA256Pointer(payloads.RequestEvent) && validSHA256Pointer(payloads.SettlementEvent) && validSHA256Pointer(payloads.AttemptCancelledEvent) && validSHA256Pointer(payloads.ReconcileCommand) && validSHA256Pointer(payloads.PropagateCommand)
}

func (store RunStore) propagatedCancellationID(rootCancellationID, childRunID string) (string, error) {
	return ids.DeterministicUUID(store.IDKey, "propagated-run-cancellation", rootCancellationID+"\x00"+childRunID)
}
