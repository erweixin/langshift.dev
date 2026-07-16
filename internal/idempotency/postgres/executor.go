// Package postgres implements the shared durable idempotency transaction
// boundary used by authenticated control-plane services.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/idempotency"
)

type Executor struct {
	Pool      *pgxpool.Pool
	KeyPepper []byte
	TTL       time.Duration
	Now       func() time.Time
}

type Input struct {
	RecordID    string
	Scope       idempotency.Scope
	RawKey      string
	RequestHash string
	RequestID   string
}

type Mutation func(context.Context, pgx.Tx) (idempotency.Response, error)

// LoadCompleted returns an exact committed replay without creating a new
// record. It is safe to use before more expensive payload preparation.
func (executor Executor) LoadCompleted(ctx context.Context, input Input) (idempotency.Response, bool, error) {
	if executor.Pool == nil || input.Scope.TenantID == "" || input.Scope.UserID == "" || input.Scope.OperationID == "" || input.RequestHash == "" {
		return idempotency.Response{}, false, idempotency.ErrInvalidKey
	}
	keyDigest, err := idempotency.KeyDigest(input.RawKey, executor.KeyPepper)
	if err != nil {
		return idempotency.Response{}, false, err
	}
	tx, err := executor.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return idempotency.Response{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.Scope.TenantID); err != nil {
		return idempotency.Response{}, false, err
	}
	var storedRequestHash string
	var response idempotency.Response
	err = tx.QueryRow(ctx, `SELECT request_hash,response_status,response_content_type,response_payload_ref,response_hash,COALESCE(resource_version,0) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4 AND status='completed'`, input.Scope.TenantID, input.Scope.UserID, input.Scope.OperationID, keyDigest[:]).Scan(&storedRequestHash, &response.Status, &response.ContentType, &response.PayloadRef, &response.Hash, &response.ResourceVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Response{}, false, nil
	}
	if err != nil {
		return idempotency.Response{}, false, err
	}
	if storedRequestHash != input.RequestHash {
		return idempotency.Response{}, false, idempotency.ErrKeyConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return idempotency.Response{}, false, err
	}
	return response, true, nil
}

// Execute serializes a tenant-principal-operation-key scope and commits the
// domain mutation and its encrypted response manifest in the same transaction.
func (executor Executor) Execute(ctx context.Context, input Input, mutation Mutation) (idempotency.Response, bool, error) {
	if executor.Pool == nil || mutation == nil || input.RecordID == "" || input.Scope.TenantID == "" || input.Scope.UserID == "" || input.Scope.OperationID == "" || input.RequestHash == "" || input.RequestID == "" || executor.TTL <= 0 {
		return idempotency.Response{}, false, idempotency.ErrInvalidKey
	}
	keyDigest, err := idempotency.KeyDigest(input.RawKey, executor.KeyPepper)
	if err != nil {
		return idempotency.Response{}, false, err
	}
	now := time.Now().UTC()
	if executor.Now != nil {
		now = executor.Now().UTC()
	}
	tx, err := executor.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return idempotency.Response{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.Scope.TenantID); err != nil {
		return idempotency.Response{}, false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.idempotency_responses (id,tenant_id,user_id,operation_id,idempotency_key_hash,request_hash,request_id,status,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'in_progress',$8) ON CONFLICT DO NOTHING`, input.RecordID, input.Scope.TenantID, input.Scope.UserID, input.Scope.OperationID, keyDigest[:], input.RequestHash, input.RequestID, now.Add(executor.TTL))
	if err != nil {
		return idempotency.Response{}, false, err
	}
	if tag.RowsAffected() == 0 {
		var storedRequestHash, status string
		err = tx.QueryRow(ctx, `SELECT request_hash,status FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4 FOR UPDATE`, input.Scope.TenantID, input.Scope.UserID, input.Scope.OperationID, keyDigest[:]).Scan(&storedRequestHash, &status)
		if err != nil {
			return idempotency.Response{}, false, err
		}
		if storedRequestHash != input.RequestHash {
			return idempotency.Response{}, false, idempotency.ErrKeyConflict
		}
		if status != "completed" {
			return idempotency.Response{}, false, idempotency.ErrInProgress
		}
		var response idempotency.Response
		err = tx.QueryRow(ctx, `SELECT response_status,response_content_type,response_payload_ref,response_hash,COALESCE(resource_version,0) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id=$3 AND idempotency_key_hash=$4`, input.Scope.TenantID, input.Scope.UserID, input.Scope.OperationID, keyDigest[:]).Scan(&response.Status, &response.ContentType, &response.PayloadRef, &response.Hash, &response.ResourceVersion)
		if err != nil {
			return idempotency.Response{}, false, err
		}
		return response, true, nil
	}
	response, err := mutation(ctx, tx)
	if err != nil {
		return idempotency.Response{}, false, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.Scope.TenantID); err != nil {
		return idempotency.Response{}, false, err
	}
	if response.Status < 100 || response.Status > 599 || response.ContentType == "" || response.PayloadRef == "" || response.Hash == "" {
		return idempotency.Response{}, false, errors.New("idempotent mutation returned an invalid response manifest")
	}
	tag, err = tx.Exec(ctx, `UPDATE agent.idempotency_responses SET status='completed',response_status=$1,response_content_type=$2,response_payload_ref=$3,response_hash=$4,resource_version=$5,completed_at=$6,version=version+1,updated_at=$6 WHERE id=$7 AND status='in_progress'`, response.Status, response.ContentType, response.PayloadRef, response.Hash, response.ResourceVersion, now, input.RecordID)
	if err != nil {
		return idempotency.Response{}, false, err
	}
	if tag.RowsAffected() != 1 {
		return idempotency.Response{}, false, idempotency.ErrInProgress
	}
	if err = tx.Commit(ctx); err != nil {
		return idempotency.Response{}, false, err
	}
	return response, false, nil
}
