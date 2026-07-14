package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type RestrictedObjectDeleter interface {
	Delete(context.Context, string) error
}

// AnonymousClaimEraser physically removes restricted blobs and clears their
// database projections. Each effect and its durable receipt are committed
// together so an unknown worker result can be reconciled without guessing.
type AnonymousClaimEraser struct {
	Pool           *pgxpool.Pool
	SystemTenantID string
	IdentityKey    []byte
	Objects        RestrictedObjectDeleter
	Now            func() time.Time
}

func (eraser AnonymousClaimEraser) Erase(ctx context.Context, saga anonymousclaim.Saga, surface string) (anonymousclaim.DeletionReceipt, error) {
	if eraser.Pool == nil || eraser.SystemTenantID == "" || len(eraser.IdentityKey) < 32 || saga.ID == "" || saga.ClaimKey == "" || saga.AnonymousSubjectID == "" || saga.Status != anonymousclaim.Erasing {
		return anonymousclaim.DeletionReceipt{}, anonymousclaim.ErrInvariant
	}
	found, err := eraser.loadReceipt(ctx, saga.ID, surface)
	if err == nil {
		return found, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return anonymousclaim.DeletionReceipt{}, err
	}
	if surface != "principal_mapping" && eraser.Objects == nil {
		return anonymousclaim.DeletionReceipt{}, anonymousclaim.ErrInvariant
	}
	now := time.Now().UTC()
	if eraser.Now != nil {
		now = eraser.Now().UTC()
	}
	receiptID, err := ids.DeterministicUUID(eraser.IdentityKey, "anonymous-erasure-receipt:"+surface, saga.ClaimKey)
	if err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	tx, err := eraser.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, eraser.SystemTenantID); err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	var lockedClaimID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM identity.onboarding_claims WHERE id=$1 AND tenant_id=$2 AND claim_key=$3 AND status='erasing' FOR UPDATE`, saga.ID, eraser.SystemTenantID, saga.ClaimKey).Scan(&lockedClaimID); err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	if found, receiptErr := eraser.loadReceiptTx(ctx, tx, saga.ID, surface); receiptErr == nil {
		return found, nil
	} else if !errors.Is(receiptErr, pgx.ErrNoRows) {
		return anonymousclaim.DeletionReceipt{}, receiptErr
	}
	details := map[string]any{"verified": true, "surface": surface}
	switch surface {
	case "body_payload":
		var encodedManifest *string
		err = tx.QueryRow(ctx, `SELECT s.experience_payload_ref FROM identity.onboarding_claims c JOIN identity.onboarding_sessions s ON s.id=c.onboarding_session_id AND s.tenant_id=c.tenant_id WHERE c.id=$1 AND c.tenant_id=$2 AND c.claim_key=$3 AND c.status='erasing' FOR UPDATE OF c,s`, saga.ID, eraser.SystemTenantID, saga.ClaimKey).Scan(&encodedManifest)
		if err != nil {
			return anonymousclaim.DeletionReceipt{}, err
		}
		deleted, deleteErr := eraser.deleteManifest(ctx, encodedManifest)
		if deleteErr != nil {
			return anonymousclaim.DeletionReceipt{}, deleteErr
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.onboarding_sessions SET experience_payload_ref=NULL,updated_at=$1 WHERE id=(SELECT onboarding_session_id FROM identity.onboarding_claims WHERE id=$2 AND tenant_id=$3) AND tenant_id=$3`, now, saga.ID, eraser.SystemTenantID); err != nil {
			return anonymousclaim.DeletionReceipt{}, err
		}
		details["objects_deleted"] = deleted
		details["projection_cleared"] = true
	case "preview_projection":
		var routeID, missionID string
		var encodedManifest *string
		err = tx.QueryRow(ctx, `SELECT r.id::text,r.mission_id::text,r.route_payload_ref FROM identity.onboarding_claims c JOIN product.route_revisions r ON r.id=c.source_route_revision_id AND r.tenant_id=c.tenant_id JOIN product.missions m ON m.id=r.mission_id AND m.tenant_id=r.tenant_id WHERE c.id=$1 AND c.tenant_id=$2 AND c.claim_key=$3 AND c.status='erasing' FOR UPDATE OF c,r,m`, saga.ID, eraser.SystemTenantID, saga.ClaimKey).Scan(&routeID, &missionID, &encodedManifest)
		if errors.Is(err, pgx.ErrNoRows) {
			// A previous attempt may have committed the projection deletion but
			// failed before returning. The receipt must then already exist.
			return eraser.loadReceiptTx(ctx, tx, saga.ID, surface)
		}
		if err != nil {
			return anonymousclaim.DeletionReceipt{}, err
		}
		deleted, deleteErr := eraser.deleteManifest(ctx, encodedManifest)
		if deleteErr != nil {
			return anonymousclaim.DeletionReceipt{}, deleteErr
		}
		tag, deleteErr := tx.Exec(ctx, `DELETE FROM product.missions WHERE id=$1 AND tenant_id=$2`, missionID, eraser.SystemTenantID)
		if deleteErr != nil || tag.RowsAffected() != 1 {
			if deleteErr != nil {
				return anonymousclaim.DeletionReceipt{}, deleteErr
			}
			return anonymousclaim.DeletionReceipt{}, anonymousclaim.ErrInvariant
		}
		details["objects_deleted"] = deleted
		details["route_revision_id_hash"] = sha256String(routeID)
		details["mission_projection_deleted"] = true
	case "principal_mapping":
		var subjectID string
		err = tx.QueryRow(ctx, `SELECT s.id::text FROM identity.onboarding_claims c JOIN identity.anonymous_subjects s ON s.id=c.anonymous_subject_id AND s.system_tenant_id=c.tenant_id WHERE c.id=$1 AND c.tenant_id=$2 AND c.claim_key=$3 AND c.status='erasing' FOR UPDATE OF c,s`, saga.ID, eraser.SystemTenantID, saga.ClaimKey).Scan(&subjectID)
		if err != nil {
			return anonymousclaim.DeletionReceipt{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.anonymous_subjects SET deleted_at=COALESCE(deleted_at,$1),updated_at=$1,version=version+1 WHERE id=$2 AND system_tenant_id=$3`, now, subjectID, eraser.SystemTenantID); err != nil {
			return anonymousclaim.DeletionReceipt{}, err
		}
		details["principal_hash"] = sha256String(subjectID)
		details["mapping_deleted"] = true
	default:
		return anonymousclaim.DeletionReceipt{}, anonymousclaim.ErrInvariant
	}
	detailJSON, err := json.Marshal(details)
	if err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	receipt := anonymousclaim.DeletionReceipt{ID: receiptID, Surface: surface, Hash: receiptHash(saga.ClaimKey, surface, detailJSON), ErasedAt: now, Details: detailJSON}
	if err = insertOrVerifyReceipt(ctx, tx, eraser.SystemTenantID, saga.ID, receipt); err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	return receipt, nil
}

func (eraser AnonymousClaimEraser) deleteManifest(ctx context.Context, encoded *string) (int, error) {
	if encoded == nil || *encoded == "" {
		return 0, nil
	}
	var manifest payload.Manifest
	if err := json.Unmarshal([]byte(*encoded), &manifest); err != nil || manifest.Ref == "" || manifest.Hash == "" {
		return 0, anonymousclaim.ErrInvariant
	}
	if err := eraser.Objects.Delete(ctx, manifest.Ref); err != nil {
		return 0, err
	}
	return 1, nil
}

func (eraser AnonymousClaimEraser) loadReceipt(ctx context.Context, claimID, surface string) (anonymousclaim.DeletionReceipt, error) {
	tx, err := eraser.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, eraser.SystemTenantID); err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	receipt, err := eraser.loadReceiptTx(ctx, tx, claimID, surface)
	if err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return anonymousclaim.DeletionReceipt{}, err
	}
	return receipt, nil
}

func (eraser AnonymousClaimEraser) loadReceiptTx(ctx context.Context, tx pgx.Tx, claimID, surface string) (anonymousclaim.DeletionReceipt, error) {
	var receipt anonymousclaim.DeletionReceipt
	err := tx.QueryRow(ctx, `SELECT id::text,surface,receipt_hash,erased_at,details FROM identity.anonymous_erasure_receipts WHERE tenant_id=$1 AND claim_id=$2 AND surface=$3`, eraser.SystemTenantID, claimID, surface).Scan(&receipt.ID, &receipt.Surface, &receipt.Hash, &receipt.ErasedAt, &receipt.Details)
	return receipt, err
}

func receiptHash(claimKey, surface string, details []byte) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("anonymous-erasure-receipt\x00"))
	_, _ = digest.Write([]byte(claimKey))
	_, _ = digest.Write([]byte("\x00" + surface + "\x00"))
	_, _ = digest.Write(details)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func sha256String(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

var _ anonymousclaim.Eraser = AnonymousClaimEraser{}
