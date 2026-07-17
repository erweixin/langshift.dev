package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/erasure"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

// AccountErasureStore is the durable coordinator for the six-surface erasure
// protocol. All tenant-scoped mutations run with FORCE RLS enabled; only the
// restore discovery function crosses tenants and it exposes no content.
type AccountErasureStore struct {
	Pool        *pgxpool.Pool
	IdentityKey []byte
	Payloads    payload.Store
	Appender    eventpostgres.Appender
	StoreEpoch  string
}

func (store AccountErasureStore) Claim(ctx context.Context, tenantID, requestID string, now time.Time) (erasure.Request, bool, error) {
	if store.Pool == nil || tenantID == "" || requestID == "" || now.IsZero() {
		return erasure.Request{}, false, erasure.ErrInvalid
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return erasure.Request{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return erasure.Request{}, false, err
	}
	request, err := loadAccountErasureRequest(ctx, tx, tenantID, requestID, true)
	if err != nil {
		return erasure.Request{}, false, err
	}
	switch request.Status {
	case "cancelled":
		return erasure.Request{}, false, erasure.ErrCancelled
	case "completed":
		if err = tx.Commit(ctx); err != nil {
			return erasure.Request{}, false, err
		}
		return request, true, nil
	case "requested", "processing":
	default:
		return erasure.Request{}, false, erasure.ErrInvalid
	}
	if now.Before(request.ScheduledFor) {
		return erasure.Request{}, false, erasure.ErrNotDue
	}
	if request.Status == "requested" {
		tag, updateErr := tx.Exec(ctx, `UPDATE identity.account_erasure_requests SET status='processing',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND status='requested'`, now.UTC(), request.ID, tenantID, request.Version)
		if updateErr != nil || tag.RowsAffected() != 1 {
			if updateErr != nil {
				return erasure.Request{}, false, updateErr
			}
			return erasure.Request{}, false, erasure.ErrInvalid
		}
		request.Status = "processing"
	}
	if err = tx.Commit(ctx); err != nil {
		return erasure.Request{}, false, err
	}
	return request, false, nil
}

func (store AccountErasureStore) LoadReceipt(ctx context.Context, tenantID, requestID string, surface erasure.Surface, recoveryEpoch string) (erasure.Receipt, bool, error) {
	if store.Pool == nil || tenantID == "" || requestID == "" || recoveryEpoch == "" {
		return erasure.Receipt{}, false, erasure.ErrInvalid
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return erasure.Receipt{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return erasure.Receipt{}, false, err
	}
	var receipt erasure.Receipt
	err = tx.QueryRow(ctx, `SELECT id::text,request_id::text,tenant_id::text,user_id::text,recovery_epoch::text,surface,receipt_hash,details,erased_at FROM identity.account_erasure_receipts WHERE tenant_id=$1 AND request_id=$2 AND recovery_epoch=$3 AND surface=$4`, tenantID, requestID, recoveryEpoch, surface).Scan(&receipt.ID, &receipt.RequestID, &receipt.TenantID, &receipt.UserID, &receipt.RecoveryEpoch, &receipt.Surface, &receipt.Hash, &receipt.Details, &receipt.ErasedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return erasure.Receipt{}, false, nil
	}
	if err != nil {
		return erasure.Receipt{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return erasure.Receipt{}, false, err
	}
	return receipt, true, nil
}

func (store AccountErasureStore) RecordReceipt(ctx context.Context, receipt erasure.Receipt) (erasure.Receipt, bool, error) {
	if store.Pool == nil || len(store.IdentityKey) < 32 || receipt.RequestID == "" || receipt.TenantID == "" || receipt.UserID == "" || receipt.RecoveryEpoch == "" || receipt.Hash == "" || receipt.ErasedAt.IsZero() {
		return erasure.Receipt{}, false, erasure.ErrInvalid
	}
	receiptID, err := ids.DeterministicUUID(store.IdentityKey, "account-erasure-receipt:"+string(receipt.Surface), receipt.RequestID+":"+receipt.RecoveryEpoch)
	if err != nil {
		return erasure.Receipt{}, false, err
	}
	receipt.ID = receiptID
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return erasure.Receipt{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, receipt.TenantID); err != nil {
		return erasure.Receipt{}, false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO identity.account_erasure_receipts(id,tenant_id,request_id,user_id,recovery_epoch,surface,receipt_hash,details,erased_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (tenant_id,request_id,recovery_epoch,surface) DO NOTHING`, receipt.ID, receipt.TenantID, receipt.RequestID, receipt.UserID, receipt.RecoveryEpoch, receipt.Surface, receipt.Hash, receipt.Details, receipt.ErasedAt.UTC())
	if err != nil {
		return erasure.Receipt{}, false, err
	}
	replayed := tag.RowsAffected() == 0
	if replayed {
		var actual erasure.Receipt
		if err = tx.QueryRow(ctx, `SELECT id::text,request_id::text,tenant_id::text,user_id::text,recovery_epoch::text,surface,receipt_hash,details,erased_at FROM identity.account_erasure_receipts WHERE tenant_id=$1 AND request_id=$2 AND recovery_epoch=$3 AND surface=$4`, receipt.TenantID, receipt.RequestID, receipt.RecoveryEpoch, receipt.Surface).Scan(&actual.ID, &actual.RequestID, &actual.TenantID, &actual.UserID, &actual.RecoveryEpoch, &actual.Surface, &actual.Hash, &actual.Details, &actual.ErasedAt); err != nil {
			return erasure.Receipt{}, false, err
		}
		if !sameAccountErasureReceipt(actual, receipt) {
			return erasure.Receipt{}, false, erasure.ErrInvalid
		}
		receipt = actual
	}
	if err = tx.Commit(ctx); err != nil {
		return erasure.Receipt{}, false, err
	}
	return receipt, replayed, nil
}

func (store AccountErasureStore) Complete(ctx context.Context, request erasure.Request, recoveryEpoch string, receipts []erasure.Receipt, now time.Time) (string, error) {
	if store.Pool == nil || len(store.IdentityKey) < 32 || store.Payloads == nil || store.StoreEpoch == "" || request.ID == "" || request.TenantID == "" || request.UserID == "" || recoveryEpoch == "" || len(receipts) != len(erasure.RequiredSurfaces) || now.IsZero() {
		return "", erasure.ErrInvalid
	}
	manifestHash, err := validateAccountErasureReceipts(receipts, request, recoveryEpoch)
	if err != nil {
		return "", err
	}
	eventID, err := ids.DeterministicUUID(store.IdentityKey, "subject-erasure-completed:event", request.ID)
	if err != nil {
		return "", err
	}
	securityEventID, _ := ids.DeterministicUUID(store.IdentityKey, "subject-erasure-completed:security-event", request.ID)
	publishOutboxID, _ := ids.DeterministicUUID(store.IdentityKey, "subject-erasure-completed:publish-outbox", request.ID)
	publishCommandID, _ := ids.DeterministicUUID(store.IdentityKey, "subject-erasure-completed:publish-command", request.ID)
	eventBody, _ := json.Marshal(map[string]any{"subject_id": request.ID, "subject_version": request.Version + 1, "request_id": request.ID, "recovery_epoch": recoveryEpoch, "receipt_manifest_hash": manifestHash, "surface_count": len(receipts)})
	eventPayload, err := store.Payloads.Put(ctx, payload.Descriptor{TenantID: request.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, eventBody)
	if err != nil {
		return "", err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, request.TenantID); err != nil {
		return "", err
	}
	locked, err := loadAccountErasureRequest(ctx, tx, request.TenantID, request.ID, true)
	if err != nil {
		return "", err
	}
	if locked.Status == "completed" {
		var existing string
		if err = tx.QueryRow(ctx, `SELECT receipt_manifest_hash FROM identity.account_erasure_requests WHERE id=$1 AND tenant_id=$2`, request.ID, request.TenantID).Scan(&existing); err != nil || existing != manifestHash {
			if err != nil {
				return "", err
			}
			return "", erasure.ErrInvalid
		}
		return existing, tx.Commit(ctx)
	}
	if locked.Status != "processing" || locked.Version != request.Version {
		return "", erasure.ErrInvalid
	}
	for _, expected := range receipts {
		var actual erasure.Receipt
		if err = tx.QueryRow(ctx, `SELECT id::text,request_id::text,tenant_id::text,user_id::text,recovery_epoch::text,surface,receipt_hash,details,erased_at FROM identity.account_erasure_receipts WHERE tenant_id=$1 AND request_id=$2 AND recovery_epoch=$3 AND surface=$4`, request.TenantID, request.ID, recoveryEpoch, expected.Surface).Scan(&actual.ID, &actual.RequestID, &actual.TenantID, &actual.UserID, &actual.RecoveryEpoch, &actual.Surface, &actual.Hash, &actual.Details, &actual.ErasedAt); err != nil || !sameAccountErasureReceipt(actual, expected) {
			if err != nil {
				return "", err
			}
			return "", erasure.ErrInvalid
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.subject_erasure_tombstones(request_id,tenant_id,user_id,initial_recovery_epoch,receipt_manifest_hash,completed_at) VALUES($1,$2,$3,$4,$5,$6)`, request.ID, request.TenantID, request.UserID, recoveryEpoch, manifestHash, now.UTC()); err != nil {
		return "", err
	}
	completionTag, updateErr := tx.Exec(ctx, `UPDATE identity.account_erasure_requests SET status='completed',version=version+1,completed_at=$1,receipt_manifest_hash=$2,updated_at=$1 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND status='processing'`, now.UTC(), manifestHash, request.ID, request.TenantID, request.Version)
	if updateErr != nil || completionTag.RowsAffected() != 1 {
		if updateErr != nil {
			return "", updateErr
		}
		return "", erasure.ErrInvalid
	}
	details, _ := json.Marshal(map[string]any{"receipt_manifest_hash": manifestHash, "recovery_epoch": recoveryEpoch, "surface_count": len(receipts)})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events(id,tenant_id,subject_user_id,event_type,request_id,details,occurred_at) VALUES($1,$2,$3,'subject_erasure_completed',$4,$5,$6)`, securityEventID, request.TenantID, request.UserID, request.ID, details, now.UTC()); err != nil {
		return "", err
	}
	actor, _ := json.Marshal(map[string]string{"kind": "system", "id": "account-erasure-worker"})
	_, err = store.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: request.TenantID, UserID: request.UserID, EventType: "SubjectErasureCompleted", SchemaVersion: 1, AggregateKind: "account_erasure_request", AggregateID: request.ID, AggregateVersion: request.Version + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now.UTC(), Actor: actor, CorrelationID: securityEventID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: publishOutboxID, CommandID: publishCommandID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return manifestHash, nil
}

func (store AccountErasureStore) PendingRestore(ctx context.Context, recoveryEpoch string, limit int) ([]erasure.Request, error) {
	if store.Pool == nil || recoveryEpoch == "" || limit < 1 || limit > 1000 {
		return nil, erasure.ErrInvalid
	}
	rows, err := store.Pool.Query(ctx, `SELECT id::text,tenant_id::text,user_id::text,version,status,scheduled_for FROM identity.list_subject_erasure_tombstones_missing_epoch($1,$2)`, recoveryEpoch, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := make([]erasure.Request, 0)
	for rows.Next() {
		var request erasure.Request
		if err = rows.Scan(&request.ID, &request.TenantID, &request.UserID, &request.Version, &request.Status, &request.ScheduledFor); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func loadAccountErasureRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID string, lock bool) (erasure.Request, error) {
	query := `SELECT id::text,tenant_id::text,user_id::text,version,status,scheduled_for FROM identity.account_erasure_requests WHERE id=$1 AND tenant_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var request erasure.Request
	err := tx.QueryRow(ctx, query, requestID, tenantID).Scan(&request.ID, &request.TenantID, &request.UserID, &request.Version, &request.Status, &request.ScheduledFor)
	return request, err
}

func sameAccountErasureReceipt(left, right erasure.Receipt) bool {
	return left.ID == right.ID && left.RequestID == right.RequestID && left.TenantID == right.TenantID && left.UserID == right.UserID && left.RecoveryEpoch == right.RecoveryEpoch && left.Surface == right.Surface && left.Hash == right.Hash && left.ErasedAt.Equal(right.ErasedAt) && equalJSON(left.Details, right.Details)
}

func validateAccountErasureReceipts(receipts []erasure.Receipt, request erasure.Request, recoveryEpoch string) (string, error) {
	seen := make(map[erasure.Surface]erasure.Receipt, len(receipts))
	for _, receipt := range receipts {
		if receipt.RequestID != request.ID || receipt.TenantID != request.TenantID || receipt.UserID != request.UserID || receipt.RecoveryEpoch != recoveryEpoch || receipt.Hash == "" {
			return "", erasure.ErrInvalid
		}
		if _, exists := seen[receipt.Surface]; exists {
			return "", erasure.ErrInvalid
		}
		seen[receipt.Surface] = receipt
	}
	for _, surface := range erasure.RequiredSurfaces {
		if _, exists := seen[surface]; !exists {
			return "", erasure.ErrInvalid
		}
	}
	// Keep this domain byte-for-byte aligned with erasure.manifestHash while
	// leaving its implementation private to the coordinator package.
	var material string
	for _, surface := range []erasure.Surface{erasure.Cache, erasure.Indexes, erasure.Memory, erasure.Payload, erasure.Snapshot, erasure.WorkspaceArtifact} {
		material += fmt.Sprintf("%s\x00%s\x00", surface, seen[surface].Hash)
	}
	digest := sha256.Sum256([]byte("lites-account-erasure-manifest-v1\x00" + material))
	return hex.EncodeToString(digest[:]), nil
}

var _ erasure.Store = AccountErasureStore{}
