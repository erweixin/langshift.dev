package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

type ReconcileRunCancellationCommand struct {
	CancellationID, TenantID, StoreEpoch string
	ExpectedCancellationVersion          uint64
	Actor                                json.RawMessage
	CorrelationID                        string
	ToolCancelledEvents                  map[string]PayloadPointer
	SettlementEvent                      PayloadPointer
	SettlementAt                         time.Time
	Child                                *ChildRunCompletion
}

type ReconciledRunCancellation struct {
	CancellationID, RunID, Status, SettlementEventID string
	CancellationVersion, RunVersion                  uint64
	CancelledToolCalls, RemainingBlockers            int
	NextAttemptAt                                    time.Time
	Settled                                          bool
}

type cancellationTool struct {
	id, userID, status, pendingCommand, effectClass string
	version                                         uint64
}

type cancellationToolIDs struct{ event, outbox, publish string }

// ReconcileCancellation cancels queued ToolCalls and, after every authoritative
// dependency and descendant has converged, atomically settles the Run, its
// active execution right, and (for child Runs) the orchestration join.
func (store RunStore) ReconcileCancellation(ctx context.Context, command ReconcileRunCancellationCommand) (ReconciledRunCancellation, error) {
	if !store.validClaim() || !validReconcileRunCancellation(command) {
		return ReconciledRunCancellation{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, command.StoreEpoch); err != nil {
		return ReconciledRunCancellation{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ReconciledRunCancellation{}, err
	}

	// Observe before locking so the transaction can follow the Worker lock
	// order: active inbox, Run, then cancellation barrier.
	var observedRunID, observedStatus string
	var observedVersion uint64
	err = tx.QueryRow(ctx, `SELECT run_id::text,status,version FROM agent.run_cancellations WHERE id=$1 AND tenant_id=$2 AND store_epoch=$3`, command.CancellationID, command.TenantID, command.StoreEpoch).Scan(&observedRunID, &observedStatus, &observedVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	if observedStatus == "settled" {
		var runVersion uint64
		var settlementEventID string
		if err = tx.QueryRow(ctx, `SELECT r.run_version,c.settlement_event_id::text FROM agent.runs r JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=$1 WHERE r.tenant_id=$2 AND r.id=$3 AND c.status='settled'`, command.CancellationID, command.TenantID, observedRunID).Scan(&runVersion, &settlementEventID); err != nil {
			return ReconciledRunCancellation{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return ReconciledRunCancellation{}, err
		}
		return ReconciledRunCancellation{CancellationID: command.CancellationID, RunID: observedRunID, Status: observedStatus, CancellationVersion: observedVersion, RunVersion: runVersion, SettlementEventID: settlementEventID, Settled: true}, nil
	}
	if observedStatus != "terminating" || observedVersion != command.ExpectedCancellationVersion {
		return ReconciledRunCancellation{}, ErrRunConflict
	}

	var observedCommand, observedAttempt sql.NullString
	if err = tx.QueryRow(ctx, `SELECT active_command_id::text,active_attempt_id::text FROM agent.runs WHERE tenant_id=$1 AND id=$2`, command.TenantID, observedRunID).Scan(&observedCommand, &observedAttempt); err != nil || observedCommand.Valid != observedAttempt.Valid {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	if observedCommand.Valid {
		var inboxID string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM agent.inbox WHERE tenant_id=$1 AND command_id=$2 AND owner_attempt_id=$3 AND status='running' FOR UPDATE`, command.TenantID, observedCommand.String, observedAttempt.String).Scan(&inboxID); err != nil {
			return ReconciledRunCancellation{}, ErrRunConflict
		}
	}

	var runStatus, runUserID, rootRunID string
	var runVersion uint64
	var activeCancellation string
	var pendingCommand, activeCommand, activeAttempt, parentRunID, childGroupID sql.NullString
	var inheritedBudget int64
	err = tx.QueryRow(ctx, `SELECT status,run_version,active_cancellation_id::text,user_id::text,pending_command_id::text,active_command_id::text,active_attempt_id::text,parent_run_id::text,root_run_id::text,child_group_id::text,inherited_budget_microunits FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, observedRunID).Scan(&runStatus, &runVersion, &activeCancellation, &runUserID, &pendingCommand, &activeCommand, &activeAttempt, &parentRunID, &rootRunID, &childGroupID, &inheritedBudget)
	if err != nil || activeCancellation != command.CancellationID || statemachine.Runs.IsTerminal(statemachine.RunState(runStatus)) || activeCommand.Valid != observedCommand.Valid || activeAttempt.Valid != observedAttempt.Valid || activeCommand.Valid && (activeCommand.String != observedCommand.String || activeAttempt.String != observedAttempt.String) {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	if err = statemachine.Runs.ValidateTransition(statemachine.RunState(runStatus), statemachine.RunCancelled); err != nil {
		return ReconciledRunCancellation{}, ErrRunConflict
	}

	var runID, status, requestEventID, settlementRef, settlementHash, attemptRef, attemptHash string
	var cancellationVersion uint64
	var propagationComplete bool
	err = tx.QueryRow(ctx, `SELECT run_id::text,status,version,request_event_id::text,settlement_payload_ref,settlement_payload_hash,COALESCE(attempt_cancelled_payload_ref,settlement_payload_ref),COALESCE(attempt_cancelled_payload_hash,settlement_payload_hash),propagation_complete FROM agent.run_cancellations WHERE id=$1 AND tenant_id=$2 AND store_epoch=$3 FOR UPDATE`, command.CancellationID, command.TenantID, command.StoreEpoch).Scan(&runID, &status, &cancellationVersion, &requestEventID, &settlementRef, &settlementHash, &attemptRef, &attemptHash, &propagationComplete)
	if err != nil || runID != observedRunID || status != "terminating" || cancellationVersion != command.ExpectedCancellationVersion {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	isChild := parentRunID.Valid
	if isChild {
		if !childGroupID.Valid || rootRunID == "" || inheritedBudget < 1 || command.SettlementEvent != (PayloadPointer{}) || !command.SettlementAt.IsZero() {
			return ReconciledRunCancellation{}, ErrInvalidCommand
		}
		if command.Child != nil && (!validChildRunCompletion(*command.Child) || command.Child.CompletedEvent.Ref != settlementRef || command.Child.CompletedEvent.Hash != settlementHash) {
			return ReconciledRunCancellation{}, ErrInvalidCommand
		}
	} else if command.Child != nil || childGroupID.Valid || rootRunID != runID || inheritedBudget != 0 {
		return ReconciledRunCancellation{}, ErrInvalidCommand
	} else if command.SettlementEvent != (PayloadPointer{}) || !command.SettlementAt.IsZero() {
		if !validSHA256Pointer(command.SettlementEvent) || command.SettlementAt.IsZero() {
			return ReconciledRunCancellation{}, ErrInvalidCommand
		}
		// The payload is encrypted before this transaction begins. Use the same
		// logical instant for the payload, terminal event, and barrier row so the
		// durable audit chain cannot disagree by materialization latency.
		now = command.SettlementAt.UTC().Truncate(time.Microsecond)
	}

	rows, err := tx.Query(ctx, `SELECT id::text,user_id::text,status,tool_call_version,pending_command_id::text,effect_class FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2 AND status IN ('requested','preview_requested','commit_requested') ORDER BY id FOR UPDATE`, command.TenantID, runID)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	tools := make([]cancellationTool, 0)
	for rows.Next() {
		var tool cancellationTool
		if err = rows.Scan(&tool.id, &tool.userID, &tool.status, &tool.version, &tool.pendingCommand, &tool.effectClass); err != nil {
			rows.Close()
			return ReconciledRunCancellation{}, err
		}
		tools = append(tools, tool)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return ReconciledRunCancellation{}, err
	}
	rows.Close()

	for _, tool := range tools {
		pointer, ok := command.ToolCancelledEvents[tool.id]
		if !ok || !validSHA256Pointer(pointer) {
			return ReconciledRunCancellation{}, ErrInvalidCommand
		}
		eventIDs, identifierErr := store.cancellationToolIdentifiers(command.CancellationID, tool.id)
		if identifierErr != nil {
			return ReconciledRunCancellation{}, identifierErr
		}
		nextVersion := tool.version + 1
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status='cancelled',tool_call_version=$1,pending_command_id=NULL,result_event_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND status=$6 AND tool_call_version=$7 AND pending_command_id=$8`, nextVersion, eventIDs.event, now, command.TenantID, tool.id, tool.status, tool.version, tool.pendingCommand); updateErr != nil {
			return ReconciledRunCancellation{}, updateErr
		} else if tag.RowsAffected() != 1 {
			return ReconciledRunCancellation{}, ErrRunConflict
		}
		if tool.effectClass != "read_only" {
			if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=version+1,status='failed',result_event_id=$1,updated_at=$2 WHERE tenant_id=$3 AND tool_call_id=$4 AND status IN ('prepared','commit_authorized')`, eventIDs.event, now, command.TenantID, tool.id); updateErr != nil {
				return ReconciledRunCancellation{}, updateErr
			} else if tag.RowsAffected() != 1 {
				return ReconciledRunCancellation{}, ErrRunConflict
			}
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, tool.pendingCommand); updateErr != nil {
			return ReconciledRunCancellation{}, updateErr
		} else if tag.RowsAffected() != 1 {
			return ReconciledRunCancellation{}, ErrRunConflict
		}
		causationID := requestEventID
		event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: command.TenantID, UserID: tool.userID, EventType: "ToolCallCancelled", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: tool.id, AggregateVersion: nextVersion, StoreEpoch: command.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, event); err != nil {
			return ReconciledRunCancellation{}, err
		}
	}

	remaining, err := countCancellationBlockers(ctx, tx, command.TenantID, runID)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	if !propagationComplete {
		remaining++
	}
	result := ReconciledRunCancellation{CancellationID: command.CancellationID, RunID: runID, Status: "terminating", CancellationVersion: cancellationVersion, RunVersion: runVersion, CancelledToolCalls: len(tools), RemainingBlockers: remaining}
	if remaining > 0 {
		nextAttempt := now.Add(cancellationReconciliationDelay).UTC().Truncate(time.Microsecond)
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=version+1,reconciliation_due_at=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='terminating'`, nextAttempt, now, command.TenantID, command.CancellationID, cancellationVersion); updateErr != nil {
			return ReconciledRunCancellation{}, updateErr
		} else if tag.RowsAffected() != 1 {
			return ReconciledRunCancellation{}, ErrRunConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return ReconciledRunCancellation{}, err
		}
		result.CancellationVersion++
		result.NextAttemptAt = nextAttempt
		return result, nil
	}
	if isChild {
		if command.Child == nil {
			return ReconciledRunCancellation{}, ErrInvalidCommand
		}
	} else if !validSHA256Pointer(command.SettlementEvent) || command.SettlementAt.IsZero() {
		return ReconciledRunCancellation{}, ErrInvalidCommand
	}

	identifiers, err := store.cancellationIdentifiers(command.CancellationID)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	attemptEvent, err := store.settleRunExecutionRight(ctx, tx, cancellationAttemptSettlement{TenantID: command.TenantID, UserID: runUserID, StoreEpoch: command.StoreEpoch, CorrelationID: command.CorrelationID, Actor: command.Actor, Payload: PayloadPointer{Ref: attemptRef, Hash: attemptHash}}, activeCommand, activeAttempt, identifiers.settlementEvent, now)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	if pendingCommand.Valid {
		if _, err = tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, pendingCommand.String); err != nil {
			return ReconciledRunCancellation{}, err
		}
	}
	nextRunVersion := runVersion + 1
	var resultSummaryRef, resultSummaryHash any
	if isChild {
		resultSummaryRef, resultSummaryHash = command.Child.ResultSummary.Ref, command.Child.ResultSummary.Hash
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='cancelled',run_version=$1,pending_command_id=NULL,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,result_summary_ref=$2,result_summary_hash=$3,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND run_version=$7 AND active_cancellation_id=$8 AND cancel_requested_at IS NOT NULL`, nextRunVersion, resultSummaryRef, resultSummaryHash, now, command.TenantID, runID, runVersion, command.CancellationID); updateErr != nil {
		return ReconciledRunCancellation{}, updateErr
	} else if tag.RowsAffected() != 1 {
		return ReconciledRunCancellation{}, ErrRunConflict
	}

	var join childJoinResult
	eventType, terminalPointer := "RunCancelled", command.SettlementEvent
	if isChild {
		eventType, terminalPointer = "ChildRunCompleted", command.Child.CompletedEvent
		join, err = store.joinCompletedChild(ctx, tx, childJoinInput{TenantID: command.TenantID, UserID: runUserID, ChildRunID: runID, ParentRunID: parentRunID.String, RootRunID: rootRunID, GroupID: childGroupID.String, ChildEventID: identifiers.settlementEvent, InheritedBudgetMicrounits: inheritedBudget, Actor: command.Actor, CorrelationID: command.CorrelationID, Completion: *command.Child, Now: now})
		if err != nil {
			return ReconciledRunCancellation{}, err
		}
	}
	causationID := requestEventID
	terminal := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.settlementEvent, TenantID: command.TenantID, UserID: runUserID, EventType: eventType, SchemaVersion: 1, AggregateKind: "run", AggregateID: runID, AggregateVersion: nextRunVersion, StoreEpoch: command.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: terminalPointer.Ref, PayloadHash: terminalPointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.settlementOutbox, CommandID: identifiers.settlementPublish, CommandType: "events.publish", PayloadRef: terminalPointer.Ref, PayloadHash: terminalPointer.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, terminal); err != nil {
		return ReconciledRunCancellation{}, err
	}
	if attemptEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *attemptEvent); err != nil {
			return ReconciledRunCancellation{}, err
		}
	}
	if join.GroupEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *join.GroupEvent); err != nil {
			return ReconciledRunCancellation{}, err
		}
	}
	if join.ParentEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *join.ParentEvent); err != nil {
			return ReconciledRunCancellation{}, err
		}
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=version+1,status='settled',settled_at=$1,settlement_event_id=$2,settlement_payload_ref=$3,settlement_payload_hash=$4,updated_at=$1 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status='terminating' AND propagation_complete=true`, now, identifiers.settlementEvent, terminalPointer.Ref, terminalPointer.Hash, command.TenantID, command.CancellationID, cancellationVersion); updateErr != nil {
		return ReconciledRunCancellation{}, updateErr
	} else if tag.RowsAffected() != 1 {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, identifiers.reconcileCommand); updateErr != nil {
		return ReconciledRunCancellation{}, updateErr
	} else if tag.RowsAffected() != 1 {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return ReconciledRunCancellation{}, err
	}
	result.Status = "settled"
	result.CancellationVersion++
	result.RunVersion = nextRunVersion
	result.SettlementEventID = identifiers.settlementEvent
	result.Settled = true
	return result, nil
}

func (store RunStore) cancellationToolIdentifiers(cancellationID, toolCallID string) (cancellationToolIDs, error) {
	values := make([]string, 3)
	for index, domain := range []string{"run-cancellation-tool-event", "run-cancellation-tool-publish-outbox", "run-cancellation-tool-publish-command"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, cancellationID+"\x00"+toolCallID)
		if err != nil {
			return cancellationToolIDs{}, err
		}
		values[index] = value
	}
	return cancellationToolIDs{values[0], values[1], values[2]}, nil
}

func validReconcileRunCancellation(command ReconcileRunCancellationCommand) bool {
	return command.CancellationID != "" && command.TenantID != "" && command.StoreEpoch != "" && command.ExpectedCancellationVersion > 0 && validJSONObject(command.Actor) && command.CorrelationID != "" && command.ToolCancelledEvents != nil && (command.Child == nil || validChildRunCompletion(*command.Child))
}
