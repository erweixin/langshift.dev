package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/platform/ids"
)

type childGroupCancellationRecoverySnapshot struct {
	id, groupID, status, cursor string
	version                     uint64
	children                    []cancellationRecoveryChild
}

func (service RunCancellationReconcilerService) ListDueChildGroupCancellationIDs(ctx context.Context, tenantID, storeEpoch, after string, limit int) ([]string, error) {
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
	rows, err := tx.Query(ctx, `SELECT id::text FROM agent.child_group_cancellations WHERE tenant_id=$1 AND store_epoch=$2 AND status='pending' AND available_at<=$3 AND (NULLIF($4,'')::uuid IS NULL OR id>NULLIF($4,'')::uuid) ORDER BY id LIMIT $5`, tenantID, storeEpoch, service.Store.claimNow(), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		values = append(values, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return values, nil
}

func (service RunCancellationReconcilerService) ReconcileDueChildGroupCancellation(ctx context.Context, tenantID, cancellationID, storeEpoch string) (CancelledChildGroupRemainder, error) {
	if !service.valid() || tenantID == "" || cancellationID == "" || storeEpoch == "" {
		return CancelledChildGroupRemainder{}, ErrConfiguration
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return CancelledChildGroupRemainder{}, err
	}
	snapshot, err := service.loadChildGroupCancellationSnapshot(ctx, tenantID, cancellationID, storeEpoch)
	if err != nil {
		return CancelledChildGroupRemainder{}, err
	}
	correlationID, err := ids.DeterministicUUID(service.IDKey, "child-group-cancellation-recovery-correlation", cancellationID)
	if err != nil {
		return CancelledChildGroupRemainder{}, err
	}
	children := make(map[string]PropagatedRunCancellationPayloads, len(snapshot.children))
	for _, child := range snapshot.children {
		if child.activeCancellation.Valid || cancellationRunTerminal(child.status) {
			continue
		}
		childCancellationID, identifierErr := service.Store.childGroupRemainderRunCancellationID(snapshot.groupID, child.id)
		if identifierErr != nil {
			return CancelledChildGroupRemainder{}, identifierErr
		}
		identifiers, identifierErr := service.Store.cancellationIdentifiers(childCancellationID)
		if identifierErr != nil {
			return CancelledChildGroupRemainder{}, identifierErr
		}
		propagation, identifierErr := service.Store.cancellationPropagationIdentifiers(childCancellationID)
		if identifierErr != nil {
			return CancelledChildGroupRemainder{}, identifierErr
		}
		attemptObjectID := childCancellationID
		if child.attempt.Valid {
			completion, completionErr := service.Store.completionEventIdentifiers(child.attempt.String, "cancelled")
			if completionErr != nil {
				return CancelledChildGroupRemainder{}, completionErr
			}
			attemptObjectID = completion.attemptEvent
		}
		requestEvent, putErr := service.putJSON(ctx, tenantID, identifiers.requestEvent, map[string]any{"subject_id": childCancellationID, "subject_version": 1, "run_id": child.id, "child_group_id": snapshot.groupID, "previous_state": child.status, "new_state": "cancellation_requested"})
		if putErr != nil {
			return CancelledChildGroupRemainder{}, putErr
		}
		settlementEvent, putErr := service.putJSON(ctx, tenantID, identifiers.settlementEvent, map[string]any{"subject_id": child.id, "subject_version": child.version + 1, "previous_state": child.status, "new_state": "cancelled", "reason_code": "child_group_policy_satisfied", "child_group_id": snapshot.groupID})
		if putErr != nil {
			return CancelledChildGroupRemainder{}, putErr
		}
		attemptEvent, putErr := service.putJSON(ctx, tenantID, attemptObjectID, map[string]any{"subject_id": child.attempt.String, "previous_state": "running", "new_state": "abandoned", "reason_code": "child_group_policy_satisfied", "run_id": child.id})
		if putErr != nil {
			return CancelledChildGroupRemainder{}, putErr
		}
		reconcileCommand, putErr := service.putJSON(ctx, tenantID, identifiers.reconcileCommand, map[string]any{"command_type": "ReconcileRunCancellation", "cancellation_id": childCancellationID, "run_id": child.id, "child_group_id": snapshot.groupID})
		if putErr != nil {
			return CancelledChildGroupRemainder{}, putErr
		}
		propagateCommand, putErr := service.putJSON(ctx, tenantID, propagation.command, map[string]any{"command_type": "PropagateRunCancellation", "cancellation_id": childCancellationID, "run_id": child.id})
		if putErr != nil {
			return CancelledChildGroupRemainder{}, putErr
		}
		children[child.id] = PropagatedRunCancellationPayloads{RequestEvent: requestEvent, SettlementEvent: settlementEvent, AttemptCancelledEvent: attemptEvent, ReconcileCommand: reconcileCommand, PropagateCommand: propagateCommand}
	}
	return service.Store.CancelChildGroupRemainder(ctx, CancelChildGroupRemainderCommand{CancellationID: cancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedVersion: snapshot.version, ExpectedCursor: snapshot.cursor, BatchSize: maximumChildGroupCancellationBatch, Actor: json.RawMessage(`{"kind":"service","id":"run-cancellation-reconciler"}`), CorrelationID: correlationID, Children: children})
}

func (service RunCancellationReconcilerService) loadChildGroupCancellationSnapshot(ctx context.Context, tenantID, cancellationID, storeEpoch string) (childGroupCancellationRecoverySnapshot, error) {
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return childGroupCancellationRecoverySnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return childGroupCancellationRecoverySnapshot{}, err
	}
	var snapshot childGroupCancellationRecoverySnapshot
	var cursor sql.NullString
	err = tx.QueryRow(ctx, `SELECT id::text,group_id::text,status,version,cursor::text FROM agent.child_group_cancellations WHERE tenant_id=$1 AND id=$2 AND store_epoch=$3 AND (status='completed' OR (status='pending' AND available_at<=$4))`, tenantID, cancellationID, storeEpoch, service.Store.claimNow()).Scan(&snapshot.id, &snapshot.groupID, &snapshot.status, &snapshot.version, &cursor)
	if err != nil {
		return childGroupCancellationRecoverySnapshot{}, err
	}
	if cursor.Valid {
		snapshot.cursor = cursor.String
	}
	if snapshot.status == "pending" {
		rows, queryErr := tx.Query(ctx, `SELECT r.id::text,r.status,r.run_version,r.active_cancellation_id::text,r.active_attempt_id::text FROM agent.child_group_members m JOIN agent.runs r ON r.tenant_id=m.tenant_id AND r.id=m.child_run_id WHERE m.tenant_id=$1 AND m.group_id=$2 AND (NULLIF($3,'')::uuid IS NULL OR r.id>NULLIF($3,'')::uuid) ORDER BY r.id LIMIT $4`, tenantID, snapshot.groupID, snapshot.cursor, maximumChildGroupCancellationBatch)
		if queryErr != nil {
			return childGroupCancellationRecoverySnapshot{}, queryErr
		}
		for rows.Next() {
			var child cancellationRecoveryChild
			if err = rows.Scan(&child.id, &child.status, &child.version, &child.activeCancellation, &child.attempt); err != nil {
				rows.Close()
				return childGroupCancellationRecoverySnapshot{}, err
			}
			snapshot.children = append(snapshot.children, child)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return childGroupCancellationRecoverySnapshot{}, err
		}
		rows.Close()
	}
	if err = tx.Commit(ctx); err != nil {
		return childGroupCancellationRecoverySnapshot{}, err
	}
	return snapshot, nil
}
