package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

func (service RepairControlService) idempotencyInput(tenantID, userID, operationID, rawKey, requestID string, canonical []byte) (repairIdempotencyInput, payload.Descriptor, error) {
	recordID, err := ids.DeterministicUUID(service.IDKey, "idempotency-record:"+operationID, tenantID+"\x00"+userID+"\x00"+rawKey)
	if err != nil {
		return repairIdempotencyInput{}, payload.Descriptor{}, err
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return repairIdempotencyInput{}, payload.Descriptor{}, err
	}
	input := repairIdempotencyInput{RecordID: recordID, TenantID: tenantID, UserID: userID, OperationID: operationID, RawKey: rawKey, RequestHash: requestHash, RequestID: requestID}
	return input, payload.Descriptor{TenantID: tenantID, ObjectID: recordID, Class: repairResponseClass, ContentType: "application/json"}, nil
}

func (service RepairControlService) beginRepairIdempotency(ctx context.Context, input repairIdempotencyInput) (bool, error) {
	digest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return false, err
	}
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
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

func (service RepairControlService) loadRepairResponse(ctx context.Context, input repairIdempotencyInput, descriptor payload.Descriptor) (api.RepairResult, bool, error) {
	digest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return api.RepairResult{}, false, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return api.RepairResult{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return api.RepairResult{}, false, err
	}
	var storedHash, ref, hash string
	var status int
	err = tx.QueryRow(ctx, `SELECT request_hash,response_status,response_payload_ref,response_hash FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4 AND status='completed'`, input.TenantID, input.UserID, input.OperationID, digest[:]).Scan(&storedHash, &status, &ref, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.RepairResult{}, false, nil
	}
	if err != nil {
		return api.RepairResult{}, false, err
	}
	if storedHash != input.RequestHash {
		return api.RepairResult{}, false, idempotency.ErrKeyConflict
	}
	if status != 200 || ref == "" || hash == "" {
		return api.RepairResult{}, false, errors.New("invalid repair idempotency response")
	}
	if err = tx.Commit(ctx); err != nil {
		return api.RepairResult{}, false, err
	}
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: ref, Hash: hash})
	if err != nil {
		return api.RepairResult{}, false, err
	}
	var result api.RepairResult
	if err = json.Unmarshal(encoded, &result); err != nil || result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		return api.RepairResult{}, false, errors.New("invalid repair response payload")
	}
	return result, true, nil
}

func (service RepairControlService) completeAndReadRepairResponse(ctx context.Context, input repairIdempotencyInput, descriptor payload.Descriptor, result api.RepairResult) (api.RepairResult, error) {
	manifest, err := service.putJSON(ctx, descriptor.TenantID, descriptor.ObjectID, descriptor.Class, result)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	digest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.idempotency_responses SET status='completed',response_status=200,response_content_type='application/json',response_payload_ref=$1,response_hash=$2,resource_version=$3,completed_at=$4,version=version+1,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND request_hash=$7 AND status='in_progress'`, manifest.Ref, manifest.Hash, result.Version, now, input.RecordID, input.TenantID, input.RequestHash)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	if tag.RowsAffected() == 0 {
		var storedHash, status string
		if err = tx.QueryRow(ctx, `SELECT request_hash,status FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4`, input.TenantID, input.UserID, input.OperationID, digest[:]).Scan(&storedHash, &status); err != nil {
			return api.RepairResult{}, api.ErrDependencyUnavailable
		}
		if storedHash != input.RequestHash {
			return api.RepairResult{}, api.ErrIdempotencyConflict
		}
		if status != "completed" {
			return api.RepairResult{}, api.ErrDependencyUnavailable
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	loaded, found, err := service.loadRepairResponse(ctx, input, descriptor)
	if err != nil || !found {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	return loaded, nil
}

func (service RepairControlService) putJSON(ctx context.Context, tenantID, objectID, class string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
	if err != nil {
		return PayloadPointer{}, err
	}
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, nil
}

func (service RepairControlService) loadRepairTarget(ctx context.Context, tenantID, toolCallID string) (repairTarget, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return repairTarget{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return repairTarget{}, err
	}
	var target repairTarget
	var toolStatus, effectStatus string
	err = tx.QueryRow(ctx, `SELECT t.id::text,t.user_id::text,t.run_id::text,t.tool_call_version,t.effect_key,e.id::text,e.version,t.status,e.status FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE t.tenant_id=$1 AND t.id=$2`, tenantID, toolCallID).Scan(&target.ToolCallID, &target.UserID, &target.RunID, &target.ToolVersion, &target.EffectKey, &target.EffectID, &target.EffectVersion, &toolStatus, &effectStatus)
	if err != nil {
		return repairTarget{}, err
	}
	if toolStatus != "outcome_unknown" || effectStatus != "outcome_unknown" || target.EffectKey == "" {
		return repairTarget{}, ErrRepairNotActionable
	}
	if err = tx.Commit(ctx); err != nil {
		return repairTarget{}, err
	}
	return target, nil
}

func (service RepairControlService) loadRepairSnapshot(ctx context.Context, tenantID, repairID string) (repairSnapshot, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return repairSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return repairSnapshot{}, err
	}
	var value repairSnapshot
	err = tx.QueryRow(ctx, `SELECT id::text,status,version,resolution,proposal_hash,evidence_hash,target_id::text,target_version,effect_version,effect_key,COALESCE(residual_risk_ref,''),initiator_user_id::text,updated_at,expires_at FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2`, tenantID, repairID).Scan(&value.ID, &value.Status, &value.Version, &value.Resolution, &value.ProposalHash, &value.EvidenceHash, &value.TargetID, &value.TargetVersion, &value.EffectVersion, &value.EffectKey, &value.ResidualRiskRef, &value.InitiatorID, &value.UpdatedAt, &value.ExpiresAt)
	if err != nil {
		return repairSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return repairSnapshot{}, err
	}
	return value, nil
}

func (service RepairControlService) loadRepairSession(ctx context.Context, tenantID, userID, sessionID string) (time.Time, string, error) {
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
	if err = tx.Commit(ctx); err != nil {
		return time.Time{}, "", err
	}
	return reauthenticatedAt.UTC().Truncate(time.Microsecond), role, nil
}

func (service RepairControlService) loadApprovalEventIDs(ctx context.Context, tenantID, repairID string) ([]string, error) {
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
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return values, nil
}

func (service RepairControlService) lockRepairDecision(ctx context.Context, repairID string) (func(), error) {
	connection, err := service.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, "repair-decision:"+repairID); err != nil {
		connection.Release()
		return nil, err
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "repair-decision:"+repairID)
		connection.Release()
	}, nil
}
