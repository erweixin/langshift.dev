package postgres

import (
	"bytes"
	"context"
	"encoding/json"

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
// only; every cancellation and ToolCall snapshot is re-read under tenant RLS.
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
	cancellationID, runID, status string
	version                       uint64
	tools                         []cancellationRecoveryTool
}

type cancellationRecoveryTool struct {
	id, status, commandID string
	version               uint64
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

// ReconcileDueCancellation materializes encrypted ToolCallCancelled payloads
// and enters the same version-fenced transaction used by command consumers.
// It never calls a tool provider and never guesses the outcome of an effect
// that has already acquired an execution right.
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
	if snapshot.status == "terminating" {
		for _, dependency := range service.Dependencies {
			if dependency == nil {
				return ReconciledRunCancellation{}, ErrConfiguration
			}
			if err = dependency.ConvergeRunCancellation(ctx, tenantID, snapshot.runID, cancellationID, storeEpoch); err != nil {
				return ReconciledRunCancellation{}, err
			}
		}
	}
	correlationID, err := ids.DeterministicUUID(service.IDKey, "run-cancellation-recovery-correlation", cancellationID)
	if err != nil {
		return ReconciledRunCancellation{}, err
	}
	pointers := make(map[string]PayloadPointer, len(snapshot.tools))
	for _, tool := range snapshot.tools {
		eventIDs, identifierErr := service.Store.cancellationToolIdentifiers(cancellationID, tool.id)
		if identifierErr != nil {
			return ReconciledRunCancellation{}, identifierErr
		}
		encoded, marshalErr := json.Marshal(map[string]any{
			"subject_id": tool.id, "subject_version": tool.version + 1,
			"previous_state": tool.status, "new_state": "cancelled",
			"reason_code": "run_cancellation_requested", "command_id": tool.commandID,
		})
		if marshalErr != nil {
			return ReconciledRunCancellation{}, marshalErr
		}
		manifest, putErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: eventIDs.event, Class: cancellationRecoveryEventClass, ContentType: "application/json"}, encoded)
		if putErr != nil {
			return ReconciledRunCancellation{}, putErr
		}
		pointers[tool.id] = PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}
	}
	return service.Store.ReconcileCancellation(ctx, ReconcileRunCancellationCommand{
		CancellationID: cancellationID, TenantID: tenantID, StoreEpoch: storeEpoch,
		ExpectedCancellationVersion: snapshot.version,
		Actor:                       json.RawMessage(`{"kind":"service","id":"run-cancellation-reconciler"}`),
		CorrelationID:               correlationID,
		ToolCancelledEvents:         pointers,
	})
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
	err = tx.QueryRow(ctx, `SELECT c.id::text,c.run_id::text,c.status,c.version
		FROM agent.run_cancellations c
		JOIN agent.events e ON e.tenant_id=c.tenant_id AND e.aggregate_kind='run_cancellation'
		  AND e.aggregate_id=c.id AND e.event_type='RunCancellationRequested' AND e.aggregate_version=1
		WHERE c.tenant_id=$1 AND c.id=$2 AND c.store_epoch=$3 AND e.store_epoch=$3
		  AND (c.status='settled' OR (c.status='terminating' AND c.reconciliation_due_at<=$4))`, tenantID, cancellationID, storeEpoch, service.Store.claimNow()).
		Scan(&snapshot.cancellationID, &snapshot.runID, &snapshot.status, &snapshot.version)
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
	defer rows.Close()
	for rows.Next() {
		var tool cancellationRecoveryTool
		if err = rows.Scan(&tool.id, &tool.status, &tool.version, &tool.commandID); err != nil {
			return cancellationRecoverySnapshot{}, err
		}
		snapshot.tools = append(snapshot.tools, tool)
	}
	if err = rows.Err(); err != nil {
		return cancellationRecoverySnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return cancellationRecoverySnapshot{}, err
	}
	return snapshot, nil
}

func (service RunCancellationReconcilerService) valid() bool {
	return service.Store.validClaim() && service.Payloads != nil && len(service.IDKey) >= 32 && bytes.Equal(service.IDKey, service.Store.IDKey)
}
