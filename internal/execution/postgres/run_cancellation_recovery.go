package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	maximumCancellationRecoveryTenantPage = 5000
	maximumCancellationRecoveryPage       = 500
	cancellationRecoveryEventClass        = "event-payload"
)

// RunCancellationReconcilerService is the background application boundary
// for cancellation convergence. Cross-tenant discovery exposes tenant IDs
// only; every cancellation, descendant, and ToolCall snapshot is re-read under
// tenant RLS before entering a version-fenced transaction.
type RunCancellationReconcilerService struct {
	Store        RunStore
	Payloads     payload.Store
	IDKey        []byte
	Dependencies []RunCancellationDependencyConverger
}

type RunCancellationDependencyConverger interface {
	ConvergeRunCancellation(context.Context, string, string, string, string) error
}

type cancellationRecoverySnapshot struct {
	cancellationID, runID, status, rootCancellationID string
	runStatus, reason                                 string
	propagationCursor, settlementRef, settlementHash  string
	parentRunID, rootRunID, childGroupID              sql.NullString
	version                                           uint64
	runVersion, cancelGeneration                      uint64
	inheritedBudget                                   int64
	remainingBlockers                                 int
	propagationComplete                               bool
	tools                                             []cancellationRecoveryTool
	children                                          []cancellationRecoveryChild
}

type cancellationRecoveryTool struct {
	id, status, commandID string
	version               uint64
}

type cancellationRecoveryChild struct {
	id, status                  string
	version                     uint64
	activeCancellation, attempt sql.NullString
}

func (service RunCancellationReconcilerService) ListDueTenantIDs(ctx context.Context, storeEpoch, after string, limit, shardIndex, shardCount int) ([]string, error) {
	if !service.valid() || storeEpoch == "" || limit < 1 || limit > maximumCancellationRecoveryTenantPage || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount {
		return nil, ErrConfiguration
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	rows, err := service.Store.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_run_cancellation_tenants($1::uuid,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, storeEpoch, after, limit, shardIndex, shardCount, service.Store.claimNow())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, limit)
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		values = append(values, tenantID)
	}
	return values, rows.Err()
}

func (service RunCancellationReconcilerService) ListDueCancellationIDs(ctx context.Context, tenantID, storeEpoch, after string, limit int) ([]string, error) {
	if !service.valid() || tenantID == "" || storeEpoch == "" || limit < 1 || limit > maximumCancellationRecoveryPage {
		return nil, ErrConfiguration
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text
		FROM agent.run_cancellations c
		JOIN agent.events e ON e.tenant_id=c.tenant_id AND e.aggregate_kind='run_cancellation'
		  AND e.aggregate_id=c.id AND e.event_type='RunCancellationRequested' AND e.aggregate_version=1
		WHERE c.tenant_id=$1 AND c.store_epoch=$2 AND e.store_epoch=$2 AND c.status IN ('requested','terminating')
		  AND c.reconciliation_due_at<=$3 AND (NULLIF($4,'')::uuid IS NULL OR c.id>NULLIF($4,'')::uuid)
		ORDER BY c.id LIMIT $5`, tenantID, storeEpoch, service.Store.claimNow(), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, limit)
	for rows.Next() {
		var cancellationID string
		if err = rows.Scan(&cancellationID); err != nil {
			return nil, err
		}
		values = append(values, cancellationID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return values, nil
}

// ReconcileDueCancellation advances at most one bounded propagation page per
// invocation. Once the whole direct-child page space is fenced, it converges
// external dependencies and enters the atomic terminal reconciliation.
func (service RunCancellationReconcilerService) ReconcileDueCancellation(ctx context.Context, tenantID, cancellationID, storeEpoch string) (ReconciledRunCancellation, error) {
	if !service.valid() || tenantID == "" || cancellationID == "" || storeEpoch == "" {
		return ReconciledRunCancellation{}, ErrConfiguration
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return ReconciledRunCancellation{}, err
	}
	snapshot, err := service.loadSnapshot(ctx, tenantID, cancellationID, storeEpoch)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	correlationID, err := ids.DeterministicUUID(service.IDKey, "run-cancellation-recovery-correlation", cancellationID)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	actor := json.RawMessage(`{"kind":"service","id":"run-cancellation-reconciler"}`)
	if snapshot.status == "settled" {
		return service.Store.ReconcileCancellation(ctx, ReconcileRunCancellationCommand{CancellationID: cancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedCancellationVersion: snapshot.version, Actor: actor, CorrelationID: correlationID, ToolCancelledEvents: map[string]PayloadPointer{}})
	}
	if snapshot.status == "terminating" && !snapshot.propagationComplete {
		children, materializeErr := service.materializePropagationPayloads(ctx, tenantID, snapshot)
		if materializeErr != nil {
			return ReconciledRunCancellation{}, materializeErr
		}
		propagated, propagateErr := service.Store.PropagateRunCancellation(ctx, PropagateRunCancellationCommand{CancellationID: cancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedCancellationVersion: snapshot.version, ExpectedCursor: snapshot.propagationCursor, BatchSize: maximumCancellationPropagationBatch, Actor: actor, CorrelationID: correlationID, Children: children})
		if propagateErr != nil {
			return ReconciledRunCancellation{}, propagateErr
		}
		return ReconciledRunCancellation{CancellationID: cancellationID, RunID: snapshot.runID, Status: "terminating", CancellationVersion: propagated.CancellationVersion, RemainingBlockers: 1}, nil
	}
	if snapshot.status == "terminating" {
		for _, dependency := range service.Dependencies {
			if dependency == nil {
				return ReconciledRunCancellation{}, ErrConfiguration
			}
			if err = dependency.ConvergeRunCancellation(ctx, tenantID, snapshot.runID, cancellationID, storeEpoch); err != nil {
				return ReconciledRunCancellation{}, err
			}
		}
		snapshot, err = service.loadSnapshot(ctx, tenantID, cancellationID, storeEpoch)
		if err != nil {
			return ReconciledRunCancellation{}, err
		}
		if snapshot.status == "settled" {
			return service.Store.ReconcileCancellation(ctx, ReconcileRunCancellationCommand{CancellationID: cancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedCancellationVersion: snapshot.version, Actor: actor, CorrelationID: correlationID, ToolCancelledEvents: map[string]PayloadPointer{}})
		}
	}
	pointers := make(map[string]PayloadPointer, len(snapshot.tools))
	for _, tool := range snapshot.tools {
		eventIDs, identifierErr := service.Store.cancellationToolIdentifiers(cancellationID, tool.id)
		if identifierErr != nil {
			return ReconciledRunCancellation{}, identifierErr
		}
		pointer, putErr := service.putJSON(ctx, tenantID, eventIDs.event, map[string]any{
			"subject_id": tool.id, "subject_version": tool.version + 1,
			"previous_state": tool.status, "new_state": "cancelled",
			"reason_code": "run_cancellation_requested", "command_id": tool.commandID,
		})
		if putErr != nil {
			return ReconciledRunCancellation{}, putErr
		}
		pointers[tool.id] = pointer
	}
	var child *ChildRunCompletion
	settlement := PayloadPointer{}
	settledAt := time.Time{}
	if snapshot.remainingBlockers == 0 && snapshot.parentRunID.Valid {
		completion, completionErr := service.materializeChildCompletion(ctx, tenantID, snapshot)
		if completionErr != nil {
			return ReconciledRunCancellation{}, completionErr
		}
		child = &completion
	} else if snapshot.remainingBlockers == 0 {
		identifiers, identifierErr := service.Store.cancellationIdentifiers(cancellationID)
		if identifierErr != nil {
			return ReconciledRunCancellation{}, identifierErr
		}
		settledAt = service.Store.claimNow()
		settlement, err = service.putJSON(ctx, tenantID, identifiers.settlementEvent, map[string]any{
			"subject_id": snapshot.runID, "subject_version": snapshot.runVersion + 1,
			"previous_state": snapshot.runStatus, "new_state": "cancelled", "reason_code": snapshot.reason,
			"command_id": nil, "run_id": snapshot.runID, "run_version": snapshot.runVersion + 1,
			"cancellation_id": snapshot.cancellationID, "cancel_generation": snapshot.cancelGeneration,
			"barrier_settled_at": settledAt,
		})
		if err != nil {
			return ReconciledRunCancellation{}, err
		}
	}
	return service.Store.ReconcileCancellation(ctx, ReconcileRunCancellationCommand{
		CancellationID: cancellationID, TenantID: tenantID, StoreEpoch: storeEpoch,
		ExpectedCancellationVersion: snapshot.version,
		Actor:                       actor,
		CorrelationID:               correlationID,
		ToolCancelledEvents:         pointers,
		SettlementEvent:             settlement,
		SettlementAt:                settledAt,
		Child:                       child,
	})
}

func (service RunCancellationReconcilerService) materializePropagationPayloads(ctx context.Context, tenantID string, snapshot cancellationRecoverySnapshot) (map[string]PropagatedRunCancellationPayloads, error) {
	result := make(map[string]PropagatedRunCancellationPayloads, len(snapshot.children))
	for _, child := range snapshot.children {
		if child.activeCancellation.Valid || cancellationRunTerminal(child.status) {
			continue
		}
		childCancellationID, err := service.Store.propagatedCancellationID(snapshot.rootCancellationID, child.id)
		if err != nil {
			return nil, err
		}
		identifiers, err := service.Store.cancellationIdentifiers(childCancellationID)
		if err != nil {
			return nil, err
		}
		propagation, err := service.Store.cancellationPropagationIdentifiers(childCancellationID)
		if err != nil {
			return nil, err
		}
		attemptObjectID := childCancellationID
		if child.attempt.Valid {
			completion, identifierErr := service.Store.completionEventIdentifiers(child.attempt.String, "cancelled")
			if identifierErr != nil {
				return nil, identifierErr
			}
			attemptObjectID = completion.attemptEvent
		}
		requestEvent, err := service.putJSON(ctx, tenantID, identifiers.requestEvent, map[string]any{"subject_id": childCancellationID, "subject_version": 1, "run_id": child.id, "root_cancellation_id": snapshot.rootCancellationID, "previous_state": child.status, "new_state": "cancellation_requested"})
		if err != nil {
			return nil, err
		}
		settlementEvent, err := service.putJSON(ctx, tenantID, identifiers.settlementEvent, map[string]any{"subject_id": child.id, "subject_version": child.version + 1, "previous_state": child.status, "new_state": "cancelled", "reason_code": "ancestor_run_cancelled", "root_cancellation_id": snapshot.rootCancellationID})
		if err != nil {
			return nil, err
		}
		attemptEvent, err := service.putJSON(ctx, tenantID, attemptObjectID, map[string]any{"subject_id": child.attempt.String, "previous_state": "running", "new_state": "abandoned", "reason_code": "ancestor_run_cancelled", "run_id": child.id})
		if err != nil {
			return nil, err
		}
		reconcileCommand, err := service.putJSON(ctx, tenantID, identifiers.reconcileCommand, map[string]any{"command_type": "ReconcileRunCancellation", "cancellation_id": childCancellationID, "run_id": child.id, "root_cancellation_id": snapshot.rootCancellationID})
		if err != nil {
			return nil, err
		}
		propagateCommand, err := service.putJSON(ctx, tenantID, propagation.command, map[string]any{"command_type": "PropagateRunCancellation", "cancellation_id": childCancellationID, "run_id": child.id, "root_cancellation_id": snapshot.rootCancellationID})
		if err != nil {
			return nil, err
		}
		result[child.id] = PropagatedRunCancellationPayloads{RequestEvent: requestEvent, SettlementEvent: settlementEvent, AttemptCancelledEvent: attemptEvent, ReconcileCommand: reconcileCommand, PropagateCommand: propagateCommand}
	}
	return result, nil
}

func (service RunCancellationReconcilerService) materializeChildCompletion(ctx context.Context, tenantID string, snapshot cancellationRecoverySnapshot) (ChildRunCompletion, error) {
	if !snapshot.parentRunID.Valid || !snapshot.childGroupID.Valid || snapshot.settlementRef == "" || snapshot.settlementHash == "" {
		return ChildRunCompletion{}, ErrConfiguration
	}
	continuation, err := service.Store.childContinuationIdentifiers(snapshot.childGroupID.String)
	if err != nil {
		return ChildRunCompletion{}, err
	}
	groupEvent, err := service.putJSON(ctx, tenantID, continuation.groupEvent, map[string]any{"subject_id": snapshot.childGroupID.String, "new_state": "joined", "child_run_id": snapshot.runID})
	if err != nil {
		return ChildRunCompletion{}, err
	}
	queuedEvent, err := service.putJSON(ctx, tenantID, continuation.runEvent, map[string]any{"subject_id": snapshot.parentRunID.String, "new_state": "queued", "child_group_id": snapshot.childGroupID.String})
	if err != nil {
		return ChildRunCompletion{}, err
	}
	resumeCommand, err := service.putJSON(ctx, tenantID, continuation.command, map[string]any{"command_type": "ResumeParentRun", "run_id": snapshot.parentRunID.String, "child_group_id": snapshot.childGroupID.String})
	if err != nil {
		return ChildRunCompletion{}, err
	}
	settlement := PayloadPointer{Ref: snapshot.settlementRef, Hash: snapshot.settlementHash}
	remainder, err := service.Store.childRemainderCancellationIdentifiers(snapshot.childGroupID.String)
	if err != nil {
		return ChildRunCompletion{}, err
	}
	cancelRemaining, err := service.putJSON(ctx, tenantID, remainder.command, map[string]any{"command_type": "CancelRemainingChildRuns", "run_id": snapshot.parentRunID.String, "child_group_id": snapshot.childGroupID.String})
	if err != nil {
		return ChildRunCompletion{}, err
	}
	return ChildRunCompletion{ResultSummary: settlement, CompletedEvent: settlement, GroupJoinedEvent: groupEvent, RunResumeQueuedEvent: queuedEvent, ResumeCommand: resumeCommand, CancelRemainingCommand: cancelRemaining, ResumeQueueClass: "background", ResumeResourceClass: "llm", ResumePriority: 70, ResumeCostUnits: 1, ResumeMaxAttempts: 5}, nil
}

func (service RunCancellationReconcilerService) putJSON(ctx context.Context, tenantID, objectID string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: cancellationRecoveryEventClass, ContentType: "application/json"}, encoded)
	if err != nil {
		return PayloadPointer{}, err
	}
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, nil
}

func (service RunCancellationReconcilerService) loadSnapshot(ctx context.Context, tenantID, cancellationID, storeEpoch string) (cancellationRecoverySnapshot, error) {
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return cancellationRecoverySnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return cancellationRecoverySnapshot{}, err
	}
	var snapshot cancellationRecoverySnapshot
	err = tx.QueryRow(ctx, `SELECT c.id::text,c.run_id::text,c.status,c.version,c.root_cancellation_id::text,c.reason,c.cancel_generation,COALESCE(c.propagation_cursor::text,''),c.propagation_complete,c.settlement_payload_ref,c.settlement_payload_hash,r.status,r.run_version,r.parent_run_id::text,r.root_run_id::text,r.child_group_id::text,r.inherited_budget_microunits
		FROM agent.run_cancellations c
		JOIN agent.runs r ON r.tenant_id=c.tenant_id AND r.id=c.run_id
		JOIN agent.events e ON e.tenant_id=c.tenant_id AND e.aggregate_kind='run_cancellation'
		  AND e.aggregate_id=c.id AND e.event_type='RunCancellationRequested' AND e.aggregate_version=1
		WHERE c.tenant_id=$1 AND c.id=$2 AND c.store_epoch=$3 AND e.store_epoch=$3
		  AND (c.status='settled' OR (c.status='terminating' AND c.reconciliation_due_at<=$4))`, tenantID, cancellationID, storeEpoch, service.Store.claimNow()).
		Scan(&snapshot.cancellationID, &snapshot.runID, &snapshot.status, &snapshot.version, &snapshot.rootCancellationID, &snapshot.reason, &snapshot.cancelGeneration, &snapshot.propagationCursor, &snapshot.propagationComplete, &snapshot.settlementRef, &snapshot.settlementHash, &snapshot.runStatus, &snapshot.runVersion, &snapshot.parentRunID, &snapshot.rootRunID, &snapshot.childGroupID, &snapshot.inheritedBudget)
	if err != nil {
		return cancellationRecoverySnapshot{}, err
	}
	if snapshot.status != "terminating" && snapshot.status != "settled" {
		return cancellationRecoverySnapshot{}, ErrRunConflict
	}
	rows, err := tx.Query(ctx, `SELECT id::text,status,tool_call_version,pending_command_id::text
		FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2
		  AND status IN ('requested','preview_requested','commit_requested') ORDER BY id`, tenantID, snapshot.runID)
	if err != nil {
		return cancellationRecoverySnapshot{}, err
	}
	for rows.Next() {
		var tool cancellationRecoveryTool
		if err = rows.Scan(&tool.id, &tool.status, &tool.version, &tool.commandID); err != nil {
			rows.Close()
			return cancellationRecoverySnapshot{}, err
		}
		snapshot.tools = append(snapshot.tools, tool)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return cancellationRecoverySnapshot{}, err
	}
	rows.Close()
	blockers, err := countCancellationBlockers(ctx, tx, tenantID, snapshot.runID)
	if err != nil {
		return cancellationRecoverySnapshot{}, err
	}
	// The reconciliation transaction cancels every ToolCall represented by
	// snapshot.tools before evaluating the barrier, so they are not remaining
	// blockers for payload-materialization purposes.
	snapshot.remainingBlockers = blockers - len(snapshot.tools)
	if snapshot.remainingBlockers < 0 {
		return cancellationRecoverySnapshot{}, ErrRunConflict
	}
	if snapshot.status == "terminating" && !snapshot.propagationComplete {
		rows, err = tx.Query(ctx, `SELECT id::text,status,run_version,active_cancellation_id::text,active_attempt_id::text FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2 AND (NULLIF($3,'')::uuid IS NULL OR id>NULLIF($3,'')::uuid) ORDER BY id LIMIT $4`, tenantID, snapshot.runID, snapshot.propagationCursor, maximumCancellationPropagationBatch)
		if err != nil {
			return cancellationRecoverySnapshot{}, err
		}
		for rows.Next() {
			var child cancellationRecoveryChild
			if err = rows.Scan(&child.id, &child.status, &child.version, &child.activeCancellation, &child.attempt); err != nil {
				rows.Close()
				return cancellationRecoverySnapshot{}, err
			}
			snapshot.children = append(snapshot.children, child)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return cancellationRecoverySnapshot{}, err
		}
		rows.Close()
	}
	if err = tx.Commit(ctx); err != nil {
		return cancellationRecoverySnapshot{}, err
	}
	return snapshot, nil
}

func cancellationRunTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled" || status == "expired"
}

func (service RunCancellationReconcilerService) valid() bool {
	return service.Store.validClaim() && service.Payloads != nil && len(service.IDKey) >= 32 && bytes.Equal(service.IDKey, service.Store.IDKey)
}
