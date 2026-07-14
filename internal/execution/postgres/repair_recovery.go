package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	maximumRepairRecoveryTenantPage = 5000
	maximumRepairRecoveryPage       = 500
)

// ListRecoverableRepairTenantIDs is the cross-tenant, epoch-fenced discovery
// boundary used by sharded repair recovery workers. The security-definer SQL
// exposes tenant identifiers only; every repair is re-read under tenant RLS.
func (service RepairControlService) ListRecoverableRepairTenantIDs(ctx context.Context, storeEpoch, after string, limit, shardIndex, shardCount int) ([]string, error) {
	if !service.valid() || storeEpoch == "" || limit < 1 || limit > maximumRepairRecoveryTenantPage || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount {
		return nil, ErrConfiguration
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	rows, err := service.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_recoverable_repair_tenants($1::uuid,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, storeEpoch, after, limit, shardIndex, shardCount, service.now())
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

// ListRecoverableRepairIDs returns approved, non-expired repairs after a
// stable UUID cursor. It does not claim ownership; ExecuteToolEffectRepair is
// itself the CAS/idempotency boundary for competing recovery workers.
func (service RepairControlService) ListRecoverableRepairIDs(ctx context.Context, tenantID, storeEpoch, after string, limit int) ([]string, error) {
	if !service.valid() || tenantID == "" || storeEpoch == "" || limit < 1 || limit > maximumRepairRecoveryPage {
		return nil, ErrConfiguration
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text FROM agent.repair_commands r JOIN agent.events e ON e.tenant_id=r.tenant_id AND e.aggregate_kind='repair_command' AND e.aggregate_id=r.id AND e.event_type='RepairCommandApproved' AND e.aggregate_version=r.version WHERE r.tenant_id=$1 AND e.store_epoch=$2 AND r.repair_kind='tool_effect_resolution' AND r.status='approved' AND r.expires_at>$3 AND (NULLIF($4,'')::uuid IS NULL OR r.id>NULLIF($4,'')::uuid) ORDER BY r.id LIMIT $5`, tenantID, storeEpoch, service.now(), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, limit)
	for rows.Next() {
		var repairID string
		if err = rows.Scan(&repairID); err != nil {
			return nil, err
		}
		values = append(values, repairID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return values, nil
}

// RecoverApprovedRepair reconstructs encrypted execution payloads from the
// immutable repair scope and enters the same atomic execution path as HTTP.
// It never calls the external provider and is safe under concurrent workers.
func (service RepairControlService) RecoverApprovedRepair(ctx context.Context, tenantID, repairID, storeEpoch string) (RepairedTool, error) {
	if !service.valid() || tenantID == "" || repairID == "" || storeEpoch == "" {
		return RepairedTool{}, ErrConfiguration
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return RepairedTool{}, err
	}
	correlationID, err := ids.DeterministicUUID(service.IDKey, "repair-recovery-correlation", repairID)
	if err != nil {
		return RepairedTool{}, err
	}
	return service.executeApprovedRepair(ctx, tenantID, repairID, correlationID)
}
