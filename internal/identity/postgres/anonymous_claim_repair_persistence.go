package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	executionapi "github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type claimRepairEventIDs struct{ event, outbox, publish string }

func (service AnonymousClaimRepairControlService) claimRepairIDs(kind, seed string) (claimRepairEventIDs, error) {
	derive := func(part string) (string, error) {
		return ids.DeterministicUUID(service.IDKey, "anonymous-claim-repair:"+kind+":"+part, seed)
	}
	eventID, err := derive("event")
	if err != nil {
		return claimRepairEventIDs{}, err
	}
	outboxID, err := derive("outbox")
	if err != nil {
		return claimRepairEventIDs{}, err
	}
	publishID, err := derive("publish")
	return claimRepairEventIDs{event: eventID, outbox: outboxID, publish: publishID}, err
}

func (service AnonymousClaimRepairControlService) claimRepairIdempotencyInput(tenantID, userID, operationID, rawKey, requestID string, canonical []byte) (claimRepairIdempotency, payload.Descriptor, error) {
	recordID, err := ids.DeterministicUUID(service.IDKey, "idempotency-record:"+operationID, tenantID+"\x00"+userID+"\x00"+rawKey)
	if err != nil {
		return claimRepairIdempotency{}, payload.Descriptor{}, err
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return claimRepairIdempotency{}, payload.Descriptor{}, err
	}
	input := claimRepairIdempotency{RecordID: recordID, TenantID: tenantID, UserID: userID, OperationID: operationID, RawKey: rawKey, RequestHash: requestHash, RequestID: requestID}
	return input, payload.Descriptor{TenantID: tenantID, ObjectID: recordID, Class: claimRepairResponseClass, ContentType: "application/json"}, nil
}

func (service AnonymousClaimRepairControlService) beginClaimRepairIdempotency(ctx context.Context, input claimRepairIdempotency) (bool, error) {
	digest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return false, err
	}
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.idempotency_responses(id,tenant_id,user_id,operation_id,idempotency_key_hash,request_hash,request_id,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,'in_progress',$8) ON CONFLICT DO NOTHING`, input.RecordID, input.TenantID, input.UserID, input.OperationID, digest[:], input.RequestHash, input.RequestID, now.Add(service.IdempotencyTTL)); err != nil {
		return false, err
	}
	var storedHash, status string
	if err = tx.QueryRow(ctx, `SELECT request_hash,status FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4`, input.TenantID, input.UserID, input.OperationID, digest[:]).Scan(&storedHash, &status); err != nil {
		return false, err
	}
	if storedHash != input.RequestHash {
		return false, idempotency.ErrKeyConflict
	}
	if status != "in_progress" && status != "completed" {
		return false, idempotency.ErrInProgress
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return status == "completed", nil
}

func (service AnonymousClaimRepairControlService) loadClaimRepairResponse(ctx context.Context, input claimRepairIdempotency, descriptor payload.Descriptor) (executionapi.RepairResult, bool, error) {
	digest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return executionapi.RepairResult{}, false, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return executionapi.RepairResult{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return executionapi.RepairResult{}, false, err
	}
	var storedHash, status, responseRef, responseHash string
	err = tx.QueryRow(ctx, `SELECT request_hash,status,COALESCE(response_payload_ref,''),COALESCE(response_hash,'') FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4`, input.TenantID, input.UserID, input.OperationID, digest[:]).Scan(&storedHash, &status, &responseRef, &responseHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionapi.RepairResult{}, false, tx.Commit(ctx)
	}
	if err != nil {
		return executionapi.RepairResult{}, false, err
	}
	if storedHash != input.RequestHash {
		return executionapi.RepairResult{}, false, idempotency.ErrKeyConflict
	}
	if status != "completed" {
		return executionapi.RepairResult{}, false, tx.Commit(ctx)
	}
	if responseRef == "" || responseHash == "" {
		return executionapi.RepairResult{}, false, errClaimRepairConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return executionapi.RepairResult{}, false, err
	}
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: responseRef, Hash: responseHash})
	if err != nil {
		return executionapi.RepairResult{}, false, err
	}
	var result executionapi.RepairResult
	if err = json.Unmarshal(encoded, &result); err != nil || result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		return executionapi.RepairResult{}, false, errClaimRepairConflict
	}
	return result, true, nil
}

func (service AnonymousClaimRepairControlService) completeClaimRepairResponse(ctx context.Context, input claimRepairIdempotency, descriptor payload.Descriptor, result executionapi.RepairResult) (executionapi.RepairResult, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return executionapi.RepairResult{}, err
	}
	manifest, err := service.Payloads.Put(ctx, descriptor, encoded)
	if err != nil {
		return executionapi.RepairResult{}, err
	}
	digest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return executionapi.RepairResult{}, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return executionapi.RepairResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return executionapi.RepairResult{}, err
	}
	now := service.now()
	tag, err := tx.Exec(ctx, `UPDATE agent.idempotency_responses SET status='completed',response_status=200,response_content_type='application/json',response_payload_ref=$1,response_hash=$2,resource_version=$3,completed_at=$4,version=version+1,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND request_hash=$7 AND status='in_progress'`, manifest.Ref, manifest.Hash, result.Version, now, input.RecordID, input.TenantID, input.RequestHash)
	if err != nil {
		return executionapi.RepairResult{}, err
	}
	if tag.RowsAffected() == 0 {
		var storedHash, status string
		if err = tx.QueryRow(ctx, `SELECT request_hash,status FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4`, input.TenantID, input.UserID, input.OperationID, digest[:]).Scan(&storedHash, &status); err != nil {
			return executionapi.RepairResult{}, err
		}
		if storedHash != input.RequestHash {
			return executionapi.RepairResult{}, idempotency.ErrKeyConflict
		}
		if status != "completed" {
			return executionapi.RepairResult{}, errClaimRepairConflict
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return executionapi.RepairResult{}, err
	}
	return service.mustLoadClaimRepairResponse(ctx, input, descriptor)
}

func (service AnonymousClaimRepairControlService) mustLoadClaimRepairResponse(ctx context.Context, input claimRepairIdempotency, descriptor payload.Descriptor) (executionapi.RepairResult, error) {
	result, found, err := service.loadClaimRepairResponse(ctx, input, descriptor)
	if err != nil || !found {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	return result, nil
}

func (service AnonymousClaimRepairControlService) abandonClaimRepairIdempotency(ctx context.Context, input claimRepairIdempotency) error {
	digest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4 AND request_hash=$5 AND status='in_progress'`, input.TenantID, input.UserID, input.OperationID, digest[:], input.RequestHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service AnonymousClaimRepairControlService) putClaimRepairJSON(ctx context.Context, tenantID, objectID, class string, value any) (claimRepairPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return claimRepairPointer{}, err
	}
	manifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
	return claimRepairPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (service AnonymousClaimRepairControlService) loadClaimRepairSnapshot(ctx context.Context, tenantID, repairID string) (claimRepairSnapshot, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return claimRepairSnapshot{}, err
	}
	var snapshot claimRepairSnapshot
	err = tx.QueryRow(ctx, `SELECT id::text,status,version,resolution,proposal_hash,evidence_hash,source_tenant_id::text,anonymous_claim_id::text,effect_key,initiator_user_id::text,target_version,updated_at,expires_at FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2 AND repair_kind='anonymous_claim_reconciliation'`, tenantID, repairID).Scan(&snapshot.ID, &snapshot.Status, &snapshot.Version, &snapshot.Resolution, &snapshot.ProposalHash, &snapshot.EvidenceHash, &snapshot.SourceTenantID, &snapshot.ClaimID, &snapshot.ClaimKey, &snapshot.InitiatorID, &snapshot.ClaimVersion, &snapshot.UpdatedAt, &snapshot.ExpiresAt)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return claimRepairSnapshot{}, err
	}
	return snapshot, nil
}

func (service AnonymousClaimRepairControlService) loadClaimRepairSession(ctx context.Context, tenantID, userID, sessionID string) (time.Time, string, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return time.Time{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return time.Time{}, "", err
	}
	var reauthenticatedAt time.Time
	var role string
	err = tx.QueryRow(ctx, `SELECT s.reauthenticated_at,m.role FROM identity.sessions s JOIN identity.memberships m ON m.tenant_id=s.active_tenant_id AND m.user_id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active_tenant_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$4 AND s.reauthenticated_at IS NOT NULL AND m.status='active'`, sessionID, userID, tenantID, service.now()).Scan(&reauthenticatedAt, &role)
	if err != nil {
		return time.Time{}, "", err
	}
	return reauthenticatedAt.UTC().Truncate(time.Microsecond), role, tx.Commit(ctx)
}

func (service AnonymousClaimRepairControlService) loadClaimRepairApprovalEventIDs(ctx context.Context, tenantID, repairID string) ([]string, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT approval_event_id::text FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND decision='approve' ORDER BY created_at,id`, tenantID, repairID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, 2)
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return values, tx.Commit(ctx)
}

func (service AnonymousClaimRepairControlService) lockClaimRepair(ctx context.Context, repairID string) (func(), error) {
	connection, err := service.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, "anonymous-claim-repair:"+repairID); err != nil {
		connection.Release()
		return nil, err
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "anonymous-claim-repair:"+repairID)
		connection.Release()
	}, nil
}
