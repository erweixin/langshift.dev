package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	behaviorapi "github.com/langshift/lites/internal/behavior/api"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

func (service ControlService) idempotencyInput(tenantID, userID, operationID, rawKey, requestID string, canonical []byte) (behaviorIdempotencyInput, payload.Descriptor, error) {
	recordID, err := ids.DeterministicUUID(service.IDKey, "idempotency-record:"+operationID, tenantID+"\x00"+userID+"\x00"+rawKey)
	if err != nil {
		return behaviorIdempotencyInput{}, payload.Descriptor{}, err
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return behaviorIdempotencyInput{}, payload.Descriptor{}, err
	}
	input := behaviorIdempotencyInput{RecordID: recordID, TenantID: tenantID, UserID: userID, OperationID: operationID, RawKey: rawKey, RequestHash: requestHash, RequestID: requestID}
	return input, payload.Descriptor{TenantID: tenantID, ObjectID: recordID, Class: behaviorResponseClass, ContentType: "application/json"}, nil
}

func (service ControlService) beginIdempotency(ctx context.Context, input behaviorIdempotencyInput) (bool, error) {
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

func (service ControlService) loadResponse(ctx context.Context, input behaviorIdempotencyInput, descriptor payload.Descriptor) (behaviorapi.MutationResult, bool, error) {
	digest, err := idempotency.KeyDigest(input.RawKey, service.IdempotencyKeyPepper)
	if err != nil {
		return behaviorapi.MutationResult{}, false, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return behaviorapi.MutationResult{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return behaviorapi.MutationResult{}, false, err
	}
	var storedHash, ref, hash string
	var status int
	err = tx.QueryRow(ctx, `SELECT request_hash,response_status,response_payload_ref,response_hash FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4 AND status='completed'`, input.TenantID, input.UserID, input.OperationID, digest[:]).Scan(&storedHash, &status, &ref, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return behaviorapi.MutationResult{}, false, nil
	}
	if err != nil {
		return behaviorapi.MutationResult{}, false, err
	}
	if storedHash != input.RequestHash {
		return behaviorapi.MutationResult{}, false, idempotency.ErrKeyConflict
	}
	if status != 200 || ref == "" || hash == "" {
		return behaviorapi.MutationResult{}, false, errors.New("invalid behavior idempotency response")
	}
	if err = tx.Commit(ctx); err != nil {
		return behaviorapi.MutationResult{}, false, err
	}
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: ref, Hash: hash})
	if err != nil {
		return behaviorapi.MutationResult{}, false, err
	}
	var result behaviorapi.MutationResult
	if err = json.Unmarshal(encoded, &result); err != nil || result.ID == "" || result.ResourceID == "" || result.EventID == "" || result.Hash == "" {
		return behaviorapi.MutationResult{}, false, errors.New("invalid behavior response payload")
	}
	return result, true, nil
}

func (service ControlService) completeAndReadResponse(ctx context.Context, input behaviorIdempotencyInput, descriptor payload.Descriptor, result behaviorapi.MutationResult) (behaviorapi.MutationResult, error) {
	manifest, err := service.putJSON(ctx, descriptor.TenantID, descriptor.ObjectID, descriptor.Class, result)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	version := result.Sequence
	if version == 0 {
		version = 1
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.idempotency_responses SET status='completed',response_status=200,response_content_type='application/json',response_payload_ref=$1,response_hash=$2,resource_version=$3,completed_at=$4,version=version+1,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND request_hash=$7 AND status='in_progress'`, manifest.Ref, manifest.Hash, version, now, input.RecordID, input.TenantID, input.RequestHash)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	if tag.RowsAffected() == 0 {
		var storedHash, status string
		if err = tx.QueryRow(ctx, `SELECT request_hash,status FROM agent.idempotency_responses WHERE id=$1 AND tenant_id=$2`, input.RecordID, input.TenantID).Scan(&storedHash, &status); err != nil || storedHash != input.RequestHash || status != "completed" {
			return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	return service.mustLoadResponse(ctx, input, descriptor)
}

func (service ControlService) mustLoadResponse(ctx context.Context, input behaviorIdempotencyInput, descriptor payload.Descriptor) (behaviorapi.MutationResult, error) {
	result, found, err := service.loadResponse(ctx, input, descriptor)
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	if !found {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	return result, nil
}

// prepareEventPayload selects one immutable encrypted envelope for the
// idempotency scope before the domain transaction. A retry after an unknown
// commit result reuses the exact ref/hash even though encryption uses random
// nonces. Concurrent losing candidates remain unreferenced immutable blobs and
// are eligible for the object-store orphan lifecycle.
func (service ControlService) prepareEventPayload(ctx context.Context, input behaviorIdempotencyInput, eventID string, value any) (PayloadPointer, error) {
	candidate, err := service.putJSON(ctx, input.TenantID, eventID, behaviorEventClass, value)
	if err != nil {
		return PayloadPointer{}, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PayloadPointer{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.TenantID); err != nil {
		return PayloadPointer{}, err
	}
	now := service.now()
	if _, err = tx.Exec(ctx, `UPDATE agent.idempotency_responses SET prepared_event_payload_ref=$1,prepared_event_payload_hash=$2,prepared_event_payload_at=$3,updated_at=$3,version=version+1 WHERE id=$4 AND tenant_id=$5 AND request_hash=$6 AND status='in_progress' AND prepared_event_payload_ref IS NULL`, candidate.Ref, candidate.Hash, now, input.RecordID, input.TenantID, input.RequestHash); err != nil {
		return PayloadPointer{}, err
	}
	var storedHash, ref, hash string
	if err = tx.QueryRow(ctx, `SELECT request_hash,prepared_event_payload_ref,prepared_event_payload_hash FROM agent.idempotency_responses WHERE id=$1 AND tenant_id=$2 AND status='in_progress'`, input.RecordID, input.TenantID).Scan(&storedHash, &ref, &hash); err != nil {
		return PayloadPointer{}, err
	}
	if storedHash != input.RequestHash || ref == "" || hash == "" {
		return PayloadPointer{}, idempotency.ErrKeyConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return PayloadPointer{}, err
	}
	return PayloadPointer{Ref: ref, Hash: hash}, nil
}

func (service ControlService) putJSON(ctx context.Context, tenantID, objectID, class string, value any) (PayloadPointer, error) {
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
