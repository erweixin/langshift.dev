package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

type ExpiredRepair struct {
	RepairID, Status, EventID string
	Version                   uint64
	ExpiredAt                 time.Time
	Replayed                  bool
}

func (service RepairControlService) ListExpiredRepairTenantIDs(ctx context.Context, storeEpoch, after string, limit, shardIndex, shardCount int) ([]string, error) {
	if service.Pool == nil || limit < 1 || limit > 5000 || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount || storeEpoch == "" {
		return nil, ErrInvalidCommand
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	rows, err := service.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_expired_repair_tenants($1,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, storeEpoch, after, limit, shardIndex, shardCount, service.now())
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

func (service RepairControlService) ListExpiredRepairIDs(ctx context.Context, tenantID, storeEpoch, after string, limit int) ([]string, error) {
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
	rows, err := tx.Query(ctx, `SELECT r.id::text FROM agent.repair_commands r JOIN agent.events e ON e.tenant_id=r.tenant_id AND e.aggregate_kind='repair_command' AND e.aggregate_id=r.id AND e.aggregate_version=1 AND e.event_type='RepairCommandProposed' WHERE r.tenant_id=$1 AND e.store_epoch=$2 AND r.status IN ('proposed','approved') AND r.expires_at<=$3 AND (NULLIF($4,'')::uuid IS NULL OR r.id>NULLIF($4,'')::uuid) ORDER BY r.id LIMIT $5`, tenantID, storeEpoch, service.now(), after, limit)
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

func (service RepairControlService) ExpireRepair(ctx context.Context, tenantID, repairID, storeEpoch, correlationID string) (ExpiredRepair, error) {
	if service.Pool == nil || service.Payloads == nil || len(service.IDKey) < 32 || tenantID == "" || repairID == "" || storeEpoch == "" || correlationID == "" {
		return ExpiredRepair{}, ErrInvalidCommand
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return ExpiredRepair{}, err
	}
	eventID, err := ids.DeterministicUUID(service.IDKey, "repair-expired:event", repairID)
	if err != nil {
		return ExpiredRepair{}, err
	}
	outboxID, err := ids.DeterministicUUID(service.IDKey, "repair-expired:outbox", repairID)
	if err != nil {
		return ExpiredRepair{}, err
	}
	publishID, err := ids.DeterministicUUID(service.IDKey, "repair-expired:publish", repairID)
	if err != nil {
		return ExpiredRepair{}, err
	}
	snapshot, err := service.loadExpirableRepair(ctx, tenantID, repairID)
	if err != nil {
		return ExpiredRepair{}, err
	}
	if snapshot.status == "expired" {
		return service.loadExpiredRepairReplay(ctx, tenantID, repairID, eventID, storeEpoch, snapshot)
	}
	pointer, err := service.putJSON(ctx, tenantID, eventID, repairEventClass, map[string]any{
		"subject_id": repairID, "subject_version": snapshot.version + 1, "previous_state": snapshot.status, "new_state": "expired", "reason_code": "approval_window_elapsed",
		"command_id": nil, "proposal_hash": snapshot.proposalHash, "repair_command_id": repairID, "target_kind": snapshot.targetKind,
		"target_id": snapshot.targetID, "target_version": snapshot.targetVersion, "expired_at": snapshot.expiresAt,
	})
	if err != nil {
		return ExpiredRepair{}, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ExpiredRepair{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return ExpiredRepair{}, err
	}
	var locked expirableRepair
	err = tx.QueryRow(ctx, `SELECT status,version,target_kind,target_id::text,target_version,proposal_hash,initiator_user_id::text,expires_at FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, repairID).Scan(&locked.status, &locked.version, &locked.targetKind, &locked.targetID, &locked.targetVersion, &locked.proposalHash, &locked.userID, &locked.expiresAt)
	if err != nil {
		return ExpiredRepair{}, err
	}
	var proposedInEpoch bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='repair_command' AND aggregate_id=$2 AND aggregate_version=1 AND event_type='RepairCommandProposed' AND store_epoch=$3)`, tenantID, repairID, storeEpoch).Scan(&proposedInEpoch); err != nil || !proposedInEpoch {
		return ExpiredRepair{}, ErrRepairNotActionable
	}
	if locked.status == "expired" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='RepairCommandExpired' AND aggregate_kind='repair_command' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5)`, tenantID, eventID, repairID, locked.version, storeEpoch).Scan(&exists); err != nil || !exists {
			return ExpiredRepair{}, ErrRepairConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return ExpiredRepair{}, err
		}
		return ExpiredRepair{RepairID: repairID, Status: "expired", Version: locked.version, EventID: eventID, ExpiredAt: locked.expiresAt, Replayed: true}, nil
	}
	now := service.now()
	if (locked.status != "proposed" && locked.status != "approved") || locked.version != snapshot.version || locked.targetKind != snapshot.targetKind || locked.targetID != snapshot.targetID || locked.targetVersion != snapshot.targetVersion || locked.proposalHash != snapshot.proposalHash || locked.userID != snapshot.userID || !locked.expiresAt.Equal(snapshot.expiresAt) || locked.expiresAt.After(now) {
		return ExpiredRepair{}, ErrRepairNotActionable
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.repair_commands SET version=version+1,status='expired',updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status IN ('proposed','approved') AND expires_at<=$1`, now, tenantID, repairID, locked.version)
	if err != nil || tag.RowsAffected() != 1 {
		return ExpiredRepair{}, ErrRepairConflict
	}
	actor, _ := json.Marshal(map[string]string{"kind": "service", "name": "repair-expiry-sweeper"})
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: tenantID, UserID: locked.userID, EventType: "RepairCommandExpired", SchemaVersion: 1, AggregateKind: "repair_command", AggregateID: repairID, AggregateVersion: locked.version + 1, StoreEpoch: storeEpoch, OccurredAt: now, Actor: actor, CorrelationID: correlationID, PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}}
	if _, err = service.Store.Appender.Append(ctx, tx, event); err != nil {
		return ExpiredRepair{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ExpiredRepair{}, err
	}
	return ExpiredRepair{RepairID: repairID, Status: "expired", Version: locked.version + 1, EventID: eventID, ExpiredAt: locked.expiresAt}, nil
}

func (service RepairControlService) loadExpiredRepairReplay(ctx context.Context, tenantID, repairID, eventID, storeEpoch string, snapshot expirableRepair) (ExpiredRepair, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return ExpiredRepair{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return ExpiredRepair{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='RepairCommandExpired' AND aggregate_kind='repair_command' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5)`, tenantID, eventID, repairID, snapshot.version, storeEpoch).Scan(&exists); err != nil || !exists {
		return ExpiredRepair{}, ErrRepairConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return ExpiredRepair{}, err
	}
	return ExpiredRepair{RepairID: repairID, Status: "expired", Version: snapshot.version, EventID: eventID, ExpiredAt: snapshot.expiresAt, Replayed: true}, nil
}

type expirableRepair struct {
	status, targetKind, targetID, proposalHash, userID string
	version, targetVersion                             uint64
	expiresAt                                          time.Time
}

func (service RepairControlService) loadExpirableRepair(ctx context.Context, tenantID, repairID string) (expirableRepair, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return expirableRepair{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return expirableRepair{}, err
	}
	var value expirableRepair
	err = tx.QueryRow(ctx, `SELECT status,version,target_kind,target_id::text,target_version,proposal_hash,initiator_user_id::text,expires_at FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2`, tenantID, repairID).Scan(&value.status, &value.version, &value.targetKind, &value.targetID, &value.targetVersion, &value.proposalHash, &value.userID, &value.expiresAt)
	if err != nil {
		return expirableRepair{}, err
	}
	if value.status == "expired" {
		return value, tx.Commit(ctx)
	}
	if (value.status != "proposed" && value.status != "approved") || value.expiresAt.After(service.now()) {
		return expirableRepair{}, ErrRepairNotActionable
	}
	return value, tx.Commit(ctx)
}
