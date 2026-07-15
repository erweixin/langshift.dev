package postgres

import (
	"context"
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
}

type ReconciledRunCancellation struct {
	CancellationID, RunID, Status, SettlementEventID string
	CancellationVersion, RunVersion                  uint64
	CancelledToolCalls, RemainingBlockers            int
	NextAttemptAt                                    time.Time
	Settled                                          bool
}

type cancellationTool struct {
	id, userID, pendingCommand, effectClass string
	version                                 uint64
}

type cancellationToolIDs struct{ event, outbox, publish string }

// ReconcileCancellation cancels only ToolCalls that have not acquired an
// execution right. Executing effects, LLM requests, runtimes, and child Runs
// remain authoritative blockers until their own fenced protocols terminate.
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

	var runID, status, requestEventID, settlementRef, settlementHash string
	var cancellationVersion uint64
	err = tx.QueryRow(ctx, `SELECT run_id::text,status,version,request_event_id::text,settlement_payload_ref,settlement_payload_hash FROM agent.run_cancellations WHERE id=$1 AND tenant_id=$2 AND store_epoch=$3 FOR UPDATE`, command.CancellationID, command.TenantID, command.StoreEpoch).Scan(&runID, &status, &cancellationVersion, &requestEventID, &settlementRef, &settlementHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	if status == "settled" {
		var runVersion uint64
		var settlementEventID string
		if err = tx.QueryRow(ctx, `SELECT r.run_version,c.settlement_event_id::text FROM agent.runs r JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=$1 WHERE r.tenant_id=$2 AND r.id=$3`, command.CancellationID, command.TenantID, runID).Scan(&runVersion, &settlementEventID); err != nil {
			return ReconciledRunCancellation{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return ReconciledRunCancellation{}, err
		}
		return ReconciledRunCancellation{CancellationID: command.CancellationID, RunID: runID, Status: status, CancellationVersion: cancellationVersion, RunVersion: runVersion, SettlementEventID: settlementEventID, Settled: true}, nil
	}
	if status != "terminating" || cancellationVersion != command.ExpectedCancellationVersion {
		return ReconciledRunCancellation{}, ErrRunConflict
	}

	rows, err := tx.Query(ctx, `SELECT id::text,user_id::text,tool_call_version,pending_command_id::text,effect_class FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2 AND status='requested' ORDER BY id FOR UPDATE`, command.TenantID, runID)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	tools := make([]cancellationTool, 0)
	for rows.Next() {
		var tool cancellationTool
		if err = rows.Scan(&tool.id, &tool.userID, &tool.version, &tool.pendingCommand, &tool.effectClass); err != nil {
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

	var runStatus, runUserID string
	var runVersion uint64
	var activeCancellation string
	err = tx.QueryRow(ctx, `SELECT status,run_version,active_cancellation_id::text,user_id::text FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, runID).Scan(&runStatus, &runVersion, &activeCancellation, &runUserID)
	if err != nil || activeCancellation != command.CancellationID || statemachine.Runs.IsTerminal(statemachine.RunState(runStatus)) {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	if err = statemachine.Runs.ValidateTransition(statemachine.RunState(runStatus), statemachine.RunCancelled); err != nil {
		return ReconciledRunCancellation{}, ErrRunConflict
	}

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
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status='cancelled',tool_call_version=$1,pending_command_id=NULL,result_event_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND status='requested' AND tool_call_version=$6 AND pending_command_id=$7`, nextVersion, eventIDs.event, now, command.TenantID, tool.id, tool.version, tool.pendingCommand); updateErr != nil || tag.RowsAffected() != 1 {
			return ReconciledRunCancellation{}, ErrRunConflict
		}
		if tool.effectClass != "read_only" {
			if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=version+1,status='failed',result_event_id=$1,updated_at=$2 WHERE tenant_id=$3 AND tool_call_id=$4 AND status='prepared'`, eventIDs.event, now, command.TenantID, tool.id); updateErr != nil || tag.RowsAffected() != 1 {
				return ReconciledRunCancellation{}, ErrRunConflict
			}
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, tool.pendingCommand); updateErr != nil || tag.RowsAffected() != 1 {
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
	result := ReconciledRunCancellation{CancellationID: command.CancellationID, RunID: runID, Status: "terminating", CancellationVersion: cancellationVersion, RunVersion: runVersion, CancelledToolCalls: len(tools), RemainingBlockers: remaining}
	if remaining > 0 {
		nextAttempt := now.Add(cancellationReconciliationDelay).UTC().Truncate(time.Microsecond)
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=version+1,reconciliation_due_at=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='terminating'`, nextAttempt, now, command.TenantID, command.CancellationID, cancellationVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return ReconciledRunCancellation{}, ErrRunConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return ReconciledRunCancellation{}, err
		}
		result.CancellationVersion++
		result.NextAttemptAt = nextAttempt
		return result, nil
	}

	identifiers, err := store.cancellationIdentifiers(command.CancellationID)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	nextRunVersion := runVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='cancelled',run_version=$1,pending_command_id=NULL,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND run_version=$5 AND active_cancellation_id=$6 AND cancel_requested_at IS NOT NULL`, nextRunVersion, now, command.TenantID, runID, runVersion, command.CancellationID); updateErr != nil || tag.RowsAffected() != 1 {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	causationID := requestEventID
	terminal := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.settlementEvent, TenantID: command.TenantID, UserID: runUserID, EventType: "RunCancelled", SchemaVersion: 1, AggregateKind: "run", AggregateID: runID, AggregateVersion: nextRunVersion, StoreEpoch: command.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: settlementRef, PayloadHash: settlementHash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.settlementOutbox, CommandID: identifiers.settlementPublish, CommandType: "events.publish", PayloadRef: settlementRef, PayloadHash: settlementHash}}}
	if _, err = store.Appender.Append(ctx, tx, terminal); err != nil {
		return ReconciledRunCancellation{}, err
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=version+1,status='settled',settled_at=$1,settlement_event_id=$2,updated_at=$1 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='terminating'`, now, identifiers.settlementEvent, command.TenantID, command.CancellationID, cancellationVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return ReconciledRunCancellation{}, ErrRunConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, identifiers.reconcileCommand); updateErr != nil || tag.RowsAffected() != 1 {
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
	return command.CancellationID != "" && command.TenantID != "" && command.StoreEpoch != "" && command.ExpectedCancellationVersion > 0 && validJSONObject(command.Actor) && command.CorrelationID != "" && command.ToolCancelledEvents != nil
}
