package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maximumPortfolioRecoveryTenantPage = 5000
	maximumPortfolioRecoveryPage       = 500

	PortfolioRecoveryQueuedRedelivery  = "queued_redelivery"
	PortfolioRecoveryExpiredRedelivery = "expired_redelivery"
	PortfolioRecoveryProjectionLag     = "projection_lag"
	PortfolioRecoveryInconsistent      = "inconsistent"
)

type PortfolioRecoveryTenantScan struct {
	AfterTenantID *string
	Limit         int
	ShardIndex    int
	ShardCount    int
	StaleAfter    time.Duration
}

type PortfolioRecoveryCandidateScan struct {
	TenantID      string
	AfterExportID *string
	Limit         int
	StaleAfter    time.Duration
}

type PortfolioRecoveryCandidate struct {
	ExportID, TenantID, UserID, RunID, StartCommandID string
	ExportStatus, RunStatus, Disposition              string
	ExportVersion, RunVersion                         uint64
	UpdatedAt                                         time.Time
	LeaseExpiresAt                                    *time.Time
}

// ListRecoverableTenantIDs is the RLS-safe cross-tenant discovery boundary.
// It returns identifiers only; candidates are always re-read under tenant RLS.
func (store PortfolioExportStore) ListRecoverableTenantIDs(ctx context.Context, scan PortfolioRecoveryTenantScan) ([]string, error) {
	if !store.valid() || scan.Limit < 1 || scan.Limit > maximumPortfolioRecoveryTenantPage || scan.ShardCount < 1 || scan.ShardIndex < 0 || scan.ShardIndex >= scan.ShardCount || scan.StaleAfter <= 0 {
		return nil, ErrConfiguration
	}
	if err := store.requireEpoch(ctx); err != nil {
		return nil, err
	}
	after := ""
	if scan.AfterTenantID != nil {
		after = *scan.AfterTenantID
	}
	now := store.now()
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_recoverable_portfolio_tenants($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7)`, store.StoreEpoch, after, scan.Limit, scan.ShardIndex, scan.ShardCount, now, now.Add(-scan.StaleAfter))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, scan.Limit)
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		values = append(values, tenantID)
	}
	return values, rows.Err()
}

// ListRecoveryCandidates classifies stalled exports without mutating them.
// Safe redelivery remains owned by CommandReconcilerStore; inconsistent rows
// are surfaced for an audited repair path rather than guessed into a state.
func (store PortfolioExportStore) ListRecoveryCandidates(ctx context.Context, scan PortfolioRecoveryCandidateScan) ([]PortfolioRecoveryCandidate, error) {
	if !store.valid() || scan.TenantID == "" || scan.Limit < 1 || scan.Limit > maximumPortfolioRecoveryPage || scan.StaleAfter <= 0 {
		return nil, ErrConfiguration
	}
	if err := store.requireEpoch(ctx); err != nil {
		return nil, err
	}
	after := ""
	if scan.AfterExportID != nil {
		after = *scan.AfterExportID
	}
	now := store.now()
	staleBefore := now.Add(-scan.StaleAfter)
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, scan.TenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,p.tenant_id::text,p.user_id::text,p.run_id::text,p.start_command_id::text,
		p.status,r.status,p.version,r.run_version,p.updated_at,r.lease_expires_at,
		CASE
		  WHEN r.status='queued' AND r.pending_command_id=p.start_command_id AND p.status='requested' THEN $6
		  WHEN r.status='executing' AND r.active_command_id=p.start_command_id AND r.lease_expires_at<=$3 THEN $7
		  WHEN p.status='requested' AND r.status='executing' AND r.active_command_id=p.start_command_id AND p.updated_at<=$4 THEN $8
		  ELSE $9
		END
		FROM product.portfolio_exports p
		JOIN agent.events e ON e.tenant_id=p.tenant_id AND e.id=p.request_event_id
		  AND e.aggregate_kind='portfolio_export' AND e.aggregate_id=p.id
		  AND e.aggregate_version=1 AND e.event_type='PortfolioExportRequested' AND e.event_schema_version=2
		JOIN agent.runs r ON r.tenant_id=p.tenant_id AND r.id=p.run_id
		WHERE p.tenant_id=$1 AND e.store_epoch=$2 AND p.status IN ('requested','building')
		  AND (
		    (r.status='queued' AND r.pending_command_id=p.start_command_id AND p.status='requested' AND p.updated_at<=$4)
		    OR (r.status='executing' AND r.active_command_id=p.start_command_id AND r.lease_expires_at<=$3)
		    OR (p.status='requested' AND r.status='executing' AND r.active_command_id=p.start_command_id AND p.updated_at<=$4)
		    OR r.status NOT IN ('queued','executing')
		    OR (r.status='queued' AND (r.pending_command_id IS DISTINCT FROM p.start_command_id OR p.status<>'requested'))
		    OR (r.status='executing' AND r.active_command_id IS DISTINCT FROM p.start_command_id)
		  )
		  AND (NULLIF($5,'')::uuid IS NULL OR p.id>NULLIF($5,'')::uuid)
		ORDER BY p.id LIMIT $10`, scan.TenantID, store.StoreEpoch, now, staleBefore, after, PortfolioRecoveryQueuedRedelivery, PortfolioRecoveryExpiredRedelivery, PortfolioRecoveryProjectionLag, PortfolioRecoveryInconsistent, scan.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]PortfolioRecoveryCandidate, 0, scan.Limit)
	for rows.Next() {
		var candidate PortfolioRecoveryCandidate
		if err = rows.Scan(&candidate.ExportID, &candidate.TenantID, &candidate.UserID, &candidate.RunID, &candidate.StartCommandID, &candidate.ExportStatus, &candidate.RunStatus, &candidate.ExportVersion, &candidate.RunVersion, &candidate.UpdatedAt, &candidate.LeaseExpiresAt, &candidate.Disposition); err != nil {
			return nil, err
		}
		values = append(values, candidate)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return values, nil
}
