package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type RecoveryTenantScan struct {
	After      string
	Limit      int
	ShardIndex int
	ShardCount int
}

func (store Store) ListReleasableTenantIDs(ctx context.Context, scan RecoveryTenantScan) ([]string, error) {
	if !store.Valid() || scan.Limit < 1 || scan.Limit > 5000 || scan.ShardCount < 1 || scan.ShardIndex < 0 || scan.ShardIndex >= scan.ShardCount {
		return nil, ErrInvalidCommand
	}
	if err := store.RequireEpoch(ctx); err != nil {
		return nil, err
	}
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id FROM contracts.list_releasable_usage_tenants($1,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, store.StoreEpoch, scan.After, scan.Limit, scan.ShardIndex, scan.ShardCount, store.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (store Store) ListReleasableReservationIDs(ctx context.Context, tenantID, after string, limit int) ([]string, error) {
	if !store.Valid() || tenantID == "" || limit < 1 || limit > 5000 {
		return nil, ErrInvalidCommand
	}
	if err := store.RequireEpoch(ctx); err != nil {
		return nil, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text FROM contracts.usage_reservations r
	JOIN agent.events e ON e.tenant_id=r.tenant_id AND e.id=r.reserved_event_id
	  AND e.aggregate_kind='usage_reservation' AND e.aggregate_id=r.id AND e.aggregate_version=1
	  AND e.event_type='UsageReserved' AND e.event_schema_version=1
	LEFT JOIN agent.llm_provider_attempts p ON p.tenant_id=r.tenant_id AND p.usage_reservation_id=r.id
	WHERE r.tenant_id=$1 AND e.store_epoch=$2 AND r.status='reserved' AND r.expires_at<=$3
	  AND (p.id IS NULL OR p.status='abandoned') AND (NULLIF($4,'')::uuid IS NULL OR r.id>NULLIF($4,'')::uuid)
	ORDER BY r.id LIMIT $5`, tenantID, store.StoreEpoch, store.now(), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var reservationID string
		if err = rows.Scan(&reservationID); err != nil {
			return nil, err
		}
		result = append(result, reservationID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
