package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const approvalEventClass = "event-payload"

type ApprovalControlService struct {
	Pool     *pgxpool.Pool
	Store    RunStore
	Payloads payload.Store
	IDKey    []byte
	Now      func() time.Time
}

type ExpiredApproval struct {
	ApprovalID, Status, EventID string
	Version                     uint64
	ExpiredAt                   time.Time
	Replayed                    bool
}

func (service ApprovalControlService) ListExpiredApprovalTenantIDs(ctx context.Context, storeEpoch, after string, limit, shardIndex, shardCount int) ([]string, error) {
	if service.Pool == nil || storeEpoch == "" || limit < 1 || limit > 5000 || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount {
		return nil, ErrInvalidCommand
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	rows, err := service.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_expired_approval_tenants($1,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, storeEpoch, after, limit, shardIndex, shardCount, service.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, limit)
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service ApprovalControlService) ListExpiredApprovalIDs(ctx context.Context, tenantID, storeEpoch, after string, limit int) ([]string, error) {
	if service.Pool == nil || tenantID == "" || storeEpoch == "" || limit < 1 || limit > 5000 {
		return nil, ErrInvalidCommand
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT a.id::text FROM agent.approvals a JOIN agent.events e ON e.tenant_id=a.tenant_id AND e.aggregate_kind='approval' AND e.aggregate_id=a.id AND e.aggregate_version=1 AND e.event_type='ApprovalRequested' WHERE a.tenant_id=$1 AND e.store_epoch=$2 AND a.status='pending' AND a.expires_at<=$3 AND (NULLIF($4,'')::uuid IS NULL OR a.id>NULLIF($4,'')::uuid) ORDER BY a.id LIMIT $5`, tenantID, storeEpoch, service.now(), after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0, limit)
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

func (service ApprovalControlService) ExpireApproval(ctx context.Context, tenantID, approvalID, storeEpoch, correlationID string) (ExpiredApproval, error) {
	if service.Pool == nil || service.Payloads == nil || len(service.IDKey) < 32 || tenantID == "" || approvalID == "" || storeEpoch == "" || correlationID == "" {
		return ExpiredApproval{}, ErrInvalidCommand
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return ExpiredApproval{}, err
	}
	eventID, err := ids.DeterministicUUID(service.IDKey, "approval-expired:event", approvalID)
	if err != nil {
		return ExpiredApproval{}, err
	}
	outboxID, err := ids.DeterministicUUID(service.IDKey, "approval-expired:outbox", approvalID)
	if err != nil {
		return ExpiredApproval{}, err
	}
	publishID, err := ids.DeterministicUUID(service.IDKey, "approval-expired:publish", approvalID)
	if err != nil {
		return ExpiredApproval{}, err
	}
	snapshot, err := service.loadExpirableApproval(ctx, tenantID, approvalID)
	if err != nil {
		return ExpiredApproval{}, err
	}
	if snapshot.status == "expired" {
		return service.loadExpiredApprovalReplay(ctx, tenantID, approvalID, eventID, storeEpoch, snapshot)
	}
	now := service.now()
	pointer, err := service.putApprovalJSON(ctx, tenantID, eventID, map[string]any{
		"subject_id": approvalID, "subject_version": snapshot.version + 1, "previous_state": snapshot.status, "new_state": "expired", "reason_code": "approval_window_elapsed",
		"proposal_hash": snapshot.proposalHash, "approval_id": approvalID, "approval_kind": snapshot.kind, "target_version": snapshot.targetVersion,
		"permission_snapshot": snapshot.permissionSnapshot, "expired_at": now,
	})
	if err != nil {
		return ExpiredApproval{}, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ExpiredApproval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return ExpiredApproval{}, err
	}
	var locked expirableApproval
	err = tx.QueryRow(ctx, `SELECT status,version,approval_kind,proposal_hash,target_version,permission_snapshot,requested_by::text,expires_at,expired_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, approvalID).Scan(&locked.status, &locked.version, &locked.kind, &locked.proposalHash, &locked.targetVersion, &locked.permissionSnapshot, &locked.userID, &locked.expiresAt, &locked.expiredAt)
	if err != nil {
		return ExpiredApproval{}, err
	}
	var requestedInEpoch bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='approval' AND aggregate_id=$2 AND aggregate_version=1 AND event_type='ApprovalRequested' AND store_epoch=$3)`, tenantID, approvalID, storeEpoch).Scan(&requestedInEpoch); err != nil || !requestedInEpoch {
		return ExpiredApproval{}, ErrApprovalNotActionable
	}
	if locked.status == "expired" {
		return service.commitExpiredApprovalReplay(ctx, tx, tenantID, approvalID, eventID, storeEpoch, locked)
	}
	if locked.status != "pending" || locked.version != snapshot.version || locked.kind != snapshot.kind || locked.proposalHash != snapshot.proposalHash || locked.targetVersion != snapshot.targetVersion || locked.permissionSnapshot != snapshot.permissionSnapshot || locked.userID != snapshot.userID || !locked.expiresAt.Equal(snapshot.expiresAt) || locked.expiresAt.After(now) {
		return ExpiredApproval{}, ErrApprovalNotActionable
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.approvals SET version=version+1,status='expired',expired_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status='pending' AND expires_at<=$1`, now, tenantID, approvalID, locked.version)
	if err != nil || tag.RowsAffected() != 1 {
		return ExpiredApproval{}, ErrApprovalConflict
	}
	actor, _ := json.Marshal(map[string]string{"kind": "service", "name": "approval-expiry-sweeper"})
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: tenantID, UserID: locked.userID, EventType: "ApprovalExpired", SchemaVersion: 1, AggregateKind: "approval", AggregateID: approvalID, AggregateVersion: locked.version + 1, StoreEpoch: storeEpoch, OccurredAt: now, Actor: actor, CorrelationID: correlationID, PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}}
	if _, err = service.Store.Appender.Append(ctx, tx, event); err != nil {
		return ExpiredApproval{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ExpiredApproval{}, err
	}
	return ExpiredApproval{ApprovalID: approvalID, Status: "expired", Version: locked.version + 1, EventID: eventID, ExpiredAt: now}, nil
}

type expirableApproval struct {
	status, kind, proposalHash, permissionSnapshot, userID string
	version, targetVersion                                 uint64
	expiresAt                                              time.Time
	expiredAt                                              *time.Time
}

func (service ApprovalControlService) loadExpirableApproval(ctx context.Context, tenantID, approvalID string) (expirableApproval, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return expirableApproval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return expirableApproval{}, err
	}
	var value expirableApproval
	err = tx.QueryRow(ctx, `SELECT status,version,approval_kind,proposal_hash,target_version,permission_snapshot,requested_by::text,expires_at,expired_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2`, tenantID, approvalID).Scan(&value.status, &value.version, &value.kind, &value.proposalHash, &value.targetVersion, &value.permissionSnapshot, &value.userID, &value.expiresAt, &value.expiredAt)
	if err != nil {
		return expirableApproval{}, err
	}
	if value.status != "expired" && (value.status != "pending" || value.expiresAt.After(service.now())) {
		return expirableApproval{}, ErrApprovalNotActionable
	}
	return value, tx.Commit(ctx)
}

func (service ApprovalControlService) loadExpiredApprovalReplay(ctx context.Context, tenantID, approvalID, eventID, storeEpoch string, snapshot expirableApproval) (ExpiredApproval, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return ExpiredApproval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return ExpiredApproval{}, err
	}
	return service.commitExpiredApprovalReplay(ctx, tx, tenantID, approvalID, eventID, storeEpoch, snapshot)
}

func (service ApprovalControlService) commitExpiredApprovalReplay(ctx context.Context, tx pgx.Tx, tenantID, approvalID, eventID, storeEpoch string, snapshot expirableApproval) (ExpiredApproval, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='ApprovalExpired' AND aggregate_kind='approval' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5)`, tenantID, eventID, approvalID, snapshot.version, storeEpoch).Scan(&exists); err != nil || !exists || snapshot.expiredAt == nil {
		return ExpiredApproval{}, ErrApprovalConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return ExpiredApproval{}, err
	}
	return ExpiredApproval{ApprovalID: approvalID, Status: "expired", Version: snapshot.version, EventID: eventID, ExpiredAt: *snapshot.expiredAt, Replayed: true}, nil
}

func (service ApprovalControlService) putApprovalJSON(ctx context.Context, tenantID, objectID string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: approvalEventClass, ContentType: "application/json"}, encoded)
	if err != nil {
		return PayloadPointer{}, err
	}
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, nil
}

func (service ApprovalControlService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
