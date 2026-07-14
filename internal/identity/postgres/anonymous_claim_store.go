package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
)

type AnonymousClaimStore struct {
	Pool           *pgxpool.Pool
	SystemTenantID string
	Now            func() time.Time
}

func (store AnonymousClaimStore) Load(ctx context.Context, claimID string) (anonymousclaim.Saga, error) {
	if store.Pool == nil || store.SystemTenantID == "" || claimID == "" {
		return anonymousclaim.Saga{}, anonymousclaim.ErrInvariant
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return anonymousclaim.Saga{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, store.SystemTenantID); err != nil {
		return anonymousclaim.Saga{}, err
	}
	var saga anonymousclaim.Saga
	var reservedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,anonymous_subject_id::text,status,version,claim_key,COALESCE(target_tenant_id::text,''),COALESCE(target_user_id::text,''),COALESCE(target_mission_id::text,''),COALESCE(destination_commit_event_id::text,''),reserved_at,expires_at FROM identity.onboarding_claims WHERE id=$1 AND tenant_id=$2`, claimID, store.SystemTenantID).Scan(&saga.ID, &saga.AnonymousSubjectID, &saga.Status, &saga.Version, &saga.ClaimKey, &saga.TargetTenantID, &saga.TargetUserID, &saga.MissionID, &saga.DestinationCommitEventID, &reservedAt, &saga.ExpiresAt)
	if err != nil {
		return anonymousclaim.Saga{}, err
	}
	if reservedAt != nil {
		saga.ReservedAt = reservedAt.UTC()
	}
	rows, err := tx.Query(ctx, `SELECT id::text,surface,receipt_hash,erased_at,details FROM identity.anonymous_erasure_receipts WHERE claim_id=$1 AND tenant_id=$2 ORDER BY surface`, claimID, store.SystemTenantID)
	if err != nil {
		return anonymousclaim.Saga{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var receipt anonymousclaim.DeletionReceipt
		if err = rows.Scan(&receipt.ID, &receipt.Surface, &receipt.Hash, &receipt.ErasedAt, &receipt.Details); err != nil {
			return anonymousclaim.Saga{}, err
		}
		saga.DeletionReceipts = append(saga.DeletionReceipts, receipt)
	}
	if err = rows.Err(); err != nil {
		return anonymousclaim.Saga{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return anonymousclaim.Saga{}, err
	}
	return saga, nil
}

func (store AnonymousClaimStore) CompareAndSwap(ctx context.Context, previous, next anonymousclaim.Saga) error {
	if store.Pool == nil || store.SystemTenantID == "" || previous.ID == "" || previous.AnonymousSubjectID == "" || next.ID != previous.ID || next.Version != previous.Version+1 {
		return anonymousclaim.ErrInvariant
	}
	if err := anonymousclaim.ValidateSuccessor(previous, next); err != nil {
		return err
	}
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, store.SystemTenantID); err != nil {
		return err
	}
	if previous.Status == anonymousclaim.Available && next.Status == anonymousclaim.Reserved {
		tag, reserveErr := tx.Exec(ctx, `UPDATE identity.anonymous_subjects SET reserved_at=COALESCE(reserved_at,$1),version=version+1,updated_at=$2 WHERE id=$3 AND system_tenant_id=$4 AND deleted_at IS NULL AND expires_at>$1`, next.ReservedAt, now, previous.AnonymousSubjectID, store.SystemTenantID)
		if reserveErr != nil {
			return reserveErr
		}
		if tag.RowsAffected() != 1 {
			return anonymousclaim.ErrVersionConflict
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE identity.onboarding_claims SET version=$1,status=$2,claim_key=$3,target_tenant_id=NULLIF($4,'')::uuid,target_user_id=NULLIF($5,'')::uuid,target_mission_id=NULLIF($6,'')::uuid,destination_commit_event_id=NULLIF($7,'')::uuid,reserved_at=CASE WHEN $2='reserved' THEN COALESCE(reserved_at,$14) ELSE reserved_at END,destination_committed_at=CASE WHEN $2='destination_committed' THEN COALESCE(destination_committed_at,$8) ELSE destination_committed_at END,erasing_at=CASE WHEN $2='erasing' THEN COALESCE(erasing_at,$8) ELSE erasing_at END,claimed_at=CASE WHEN $2='claimed' THEN COALESCE(claimed_at,$8) ELSE claimed_at END,updated_at=$8 WHERE id=$9 AND tenant_id=$10 AND version=$11 AND status=$12 AND claim_key=$13 AND ($2<>'expired' OR expires_at<=$8)`, next.Version, next.Status, next.ClaimKey, next.TargetTenantID, next.TargetUserID, next.MissionID, next.DestinationCommitEventID, now, previous.ID, store.SystemTenantID, previous.Version, previous.Status, previous.ClaimKey, next.ReservedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return anonymousclaim.ErrVersionConflict
	}
	if receipt, found := newReceipt(previous, next); found {
		if _, err = tx.Exec(ctx, `INSERT INTO identity.anonymous_erasure_receipts(id,tenant_id,claim_id,surface,receipt_hash,erased_at,details) VALUES($1,$2,$3,$4,$5,$6,$7)`, receipt.ID, store.SystemTenantID, previous.ID, receipt.Surface, receipt.Hash, receipt.ErasedAt.UTC(), receipt.Details); err != nil {
			return err
		}
	}
	if next.Status == anonymousclaim.Claimed {
		if _, err = tx.Exec(ctx, `UPDATE identity.anonymous_subjects SET deleted_at=COALESCE(deleted_at,$1),version=version+1,updated_at=$1 WHERE id=$2 AND system_tenant_id=$3`, now, previous.AnonymousSubjectID, store.SystemTenantID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func newReceipt(previous, next anonymousclaim.Saga) (anonymousclaim.DeletionReceipt, bool) {
	for _, receipt := range next.DeletionReceipts {
		if !anonymousclaim.HasDeletionReceipt(previous, receipt.Surface) {
			return receipt, true
		}
	}
	return anonymousclaim.DeletionReceipt{}, false
}

var _ anonymousclaim.Store = AnonymousClaimStore{}
