package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type RecoveryCandidate struct {
	AttemptID, Status, Disposition string
	Version                        uint64
	DueAt                          time.Time
}

const (
	RecoveryAbandonPrepared = "abandon_prepared"
	RecoveryMarkUnknown     = "mark_outcome_unknown"
	RecoveryReconcileUsage  = "reconcile_usage"
)

func (store Store) ListRecoveryCandidates(ctx context.Context, tenantID, after string, limit int) ([]RecoveryCandidate, error) {
	if !store.valid() || tenantID == "" || limit < 1 || limit > 5000 {
		return nil, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return nil, err
	}
	now := store.now()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,p.status,p.version,
	  CASE WHEN p.status='prepared' THEN 'abandon_prepared' WHEN p.status='dispatching' THEN 'mark_outcome_unknown' ELSE 'reconcile_usage' END,
	  CASE WHEN p.status='prepared' THEN p.prepare_token_expires_at WHEN p.status='dispatching' THEN p.completion_deadline ELSE p.reconciliation_due_at END
	FROM agent.llm_provider_attempts p JOIN agent.events e ON e.tenant_id=p.tenant_id AND e.id=p.prepared_event_id
	WHERE p.tenant_id=$1 AND e.store_epoch=$2 AND e.aggregate_kind='provider_attempt' AND e.aggregate_id=p.id
	  AND e.aggregate_version=1 AND e.event_type='ProviderAttemptPrepared' AND e.event_schema_version=1
	  AND ((p.status='prepared' AND p.prepare_token_expires_at<=$3) OR (p.status='dispatching' AND p.completion_deadline<=$3)
	    OR (p.status='outcome_unknown' AND p.version=3 AND p.reconciliation_due_at<=$3))
	  AND (NULLIF($4,'')::uuid IS NULL OR p.id>NULLIF($4,'')::uuid)
	ORDER BY p.id LIMIT $5`, tenantID, store.StoreEpoch, now, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RecoveryCandidate
	for rows.Next() {
		var candidate RecoveryCandidate
		if err = rows.Scan(&candidate.AttemptID, &candidate.Status, &candidate.Version, &candidate.Disposition, &candidate.DueAt); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return nil, err
	}
	return result, nil
}
