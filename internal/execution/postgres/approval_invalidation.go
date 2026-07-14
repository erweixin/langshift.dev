package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

type InvalidatedApproval struct {
	ApprovalID, Status, EventID string
	Version                     uint64
	InvalidatedAt               time.Time
	Replayed                    bool
}

type approvalScopeSnapshot struct {
	status, kind, runID, toolID, proposalHash, permissionSnapshot, userID string
	version, targetVersion                                                uint64
	updatedAt, expiresAt                                                  time.Time
}

func (service ApprovalControlService) ListStaleApprovalTenantIDs(ctx context.Context, storeEpoch, after string, limit, shardIndex, shardCount int) ([]string, error) {
	if service.Pool == nil || storeEpoch == "" || limit < 1 || limit > 5000 || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount {
		return nil, ErrInvalidCommand
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	rows, err := service.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_stale_approval_tenants($1,NULLIF($2,'')::uuid,$3,$4,$5)`, storeEpoch, after, limit, shardIndex, shardCount)
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

func (service ApprovalControlService) ListStaleApprovalIDs(ctx context.Context, tenantID, storeEpoch, after string, limit int) ([]string, error) {
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
	rows, err := tx.Query(ctx, `SELECT a.id::text FROM agent.approvals a JOIN agent.events e ON e.tenant_id=a.tenant_id AND e.aggregate_kind='approval' AND e.aggregate_id=a.id AND e.aggregate_version=1 AND e.event_type='ApprovalRequested' LEFT JOIN identity.memberships m ON m.tenant_id=a.tenant_id AND m.user_id=a.requested_by AND m.status='active' LEFT JOIN agent.tool_calls t ON a.approval_kind='tool_execution' AND t.tenant_id=a.tenant_id AND t.id=a.tool_call_id LEFT JOIN agent.runs r ON a.approval_kind='stage_checkpoint' AND r.tenant_id=a.tenant_id AND r.id=a.run_id WHERE a.tenant_id=$1 AND e.store_epoch=$2 AND a.status='pending' AND (NULLIF($3,'')::uuid IS NULL OR a.id>NULLIF($3,'')::uuid) AND (m.id IS NULL OR a.permission_snapshot<>format('membership:%s:v%s:role:%s',m.id,m.version,m.role) OR (a.approval_kind='tool_execution' AND (t.id IS NULL OR t.status<>'awaiting_approval' OR t.tool_call_version<>a.target_version)) OR (a.approval_kind='stage_checkpoint' AND (r.id IS NULL OR r.status<>'waiting_approval' OR r.run_version<>a.target_version))) ORDER BY a.id LIMIT $4`, tenantID, storeEpoch, after, limit)
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

func (service ApprovalControlService) InvalidateApprovalScope(ctx context.Context, tenantID, approvalID, storeEpoch, correlationID string) (InvalidatedApproval, error) {
	if service.Pool == nil || service.Payloads == nil || len(service.IDKey) < 32 || tenantID == "" || approvalID == "" || storeEpoch == "" || correlationID == "" {
		return InvalidatedApproval{}, ErrInvalidCommand
	}
	if err := service.Store.requireClaimEpoch(ctx, storeEpoch); err != nil {
		return InvalidatedApproval{}, err
	}
	eventID, err := ids.DeterministicUUID(service.IDKey, "approval-invalidated:event", approvalID)
	if err != nil {
		return InvalidatedApproval{}, err
	}
	outboxID, err := ids.DeterministicUUID(service.IDKey, "approval-invalidated:outbox", approvalID)
	if err != nil {
		return InvalidatedApproval{}, err
	}
	publishID, err := ids.DeterministicUUID(service.IDKey, "approval-invalidated:publish", approvalID)
	if err != nil {
		return InvalidatedApproval{}, err
	}
	candidate, reason, err := service.loadApprovalScopeCandidate(ctx, tenantID, approvalID, storeEpoch)
	if err != nil {
		return InvalidatedApproval{}, err
	}
	if candidate.status == "invalidated" {
		return service.loadInvalidatedApprovalReplay(ctx, tenantID, approvalID, eventID, storeEpoch)
	}
	if reason == "" {
		return InvalidatedApproval{}, ErrApprovalNotActionable
	}
	now := service.now()
	pointer, err := service.putApprovalJSON(ctx, tenantID, eventID, map[string]any{"subject_id": approvalID, "subject_version": candidate.version + 1, "previous_state": candidate.status, "new_state": "invalidated", "reason_code": reason, "proposal_hash": candidate.proposalHash})
	if err != nil {
		return InvalidatedApproval{}, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return InvalidatedApproval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return InvalidatedApproval{}, err
	}
	var snapshot approvalScopeSnapshot
	err = tx.QueryRow(ctx, `SELECT status,version,approval_kind,COALESCE(run_id::text,''),COALESCE(tool_call_id::text,''),proposal_hash,target_version,permission_snapshot,requested_by::text,updated_at,expires_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, approvalID).Scan(&snapshot.status, &snapshot.version, &snapshot.kind, &snapshot.runID, &snapshot.toolID, &snapshot.proposalHash, &snapshot.targetVersion, &snapshot.permissionSnapshot, &snapshot.userID, &snapshot.updatedAt, &snapshot.expiresAt)
	if err != nil {
		return InvalidatedApproval{}, err
	}
	if snapshot.status == "invalidated" {
		return service.commitInvalidatedApprovalReplay(ctx, tx, tenantID, approvalID, eventID, storeEpoch, snapshot)
	}
	if snapshot.status != "pending" && snapshot.status != "granted" {
		return InvalidatedApproval{}, ErrApprovalNotActionable
	}
	if !sameApprovalScope(snapshot, candidate) {
		return InvalidatedApproval{}, ErrApprovalNotActionable
	}
	var requestedInEpoch bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='approval' AND aggregate_id=$2 AND aggregate_version=1 AND event_type='ApprovalRequested' AND store_epoch=$3)`, tenantID, approvalID, storeEpoch).Scan(&requestedInEpoch); err != nil {
		return InvalidatedApproval{}, err
	}
	if !requestedInEpoch {
		return InvalidatedApproval{}, ErrApprovalNotActionable
	}
	lockedReason, err := approvalScopeDriftReason(ctx, tx, tenantID, snapshot, true)
	if err != nil {
		return InvalidatedApproval{}, err
	}
	if lockedReason == "" || lockedReason != reason {
		return InvalidatedApproval{}, ErrApprovalNotActionable
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.approvals SET version=version+1,status='invalidated',invalidated_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status=$5`, now, tenantID, approvalID, snapshot.version, snapshot.status)
	if err != nil {
		return InvalidatedApproval{}, err
	}
	if tag.RowsAffected() != 1 {
		return InvalidatedApproval{}, ErrApprovalConflict
	}
	actor, _ := json.Marshal(map[string]string{"kind": "service", "name": "approval-scope-sweeper"})
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: tenantID, UserID: snapshot.userID, EventType: "ApprovalInvalidated", SchemaVersion: 1, AggregateKind: "approval", AggregateID: approvalID, AggregateVersion: snapshot.version + 1, StoreEpoch: storeEpoch, OccurredAt: now, Actor: actor, CorrelationID: correlationID, PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}}
	if _, err = service.Store.Appender.Append(ctx, tx, event); err != nil {
		return InvalidatedApproval{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return InvalidatedApproval{}, err
	}
	return InvalidatedApproval{ApprovalID: approvalID, Status: "invalidated", Version: snapshot.version + 1, EventID: eventID, InvalidatedAt: now}, nil
}

func (service ApprovalControlService) loadApprovalScopeCandidate(ctx context.Context, tenantID, approvalID, storeEpoch string) (approvalScopeSnapshot, string, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return approvalScopeSnapshot{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return approvalScopeSnapshot{}, "", err
	}
	var snapshot approvalScopeSnapshot
	err = tx.QueryRow(ctx, `SELECT status,version,approval_kind,COALESCE(run_id::text,''),COALESCE(tool_call_id::text,''),proposal_hash,target_version,permission_snapshot,requested_by::text,updated_at,expires_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2`, tenantID, approvalID).Scan(&snapshot.status, &snapshot.version, &snapshot.kind, &snapshot.runID, &snapshot.toolID, &snapshot.proposalHash, &snapshot.targetVersion, &snapshot.permissionSnapshot, &snapshot.userID, &snapshot.updatedAt, &snapshot.expiresAt)
	if err != nil {
		return approvalScopeSnapshot{}, "", err
	}
	var requestedInEpoch bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='approval' AND aggregate_id=$2 AND aggregate_version=1 AND event_type='ApprovalRequested' AND store_epoch=$3)`, tenantID, approvalID, storeEpoch).Scan(&requestedInEpoch); err != nil {
		return approvalScopeSnapshot{}, "", err
	}
	if !requestedInEpoch {
		return approvalScopeSnapshot{}, "", ErrApprovalNotActionable
	}
	if snapshot.status == "invalidated" {
		return snapshot, "", tx.Commit(ctx)
	}
	if snapshot.status != "pending" && snapshot.status != "granted" {
		return approvalScopeSnapshot{}, "", ErrApprovalNotActionable
	}
	reason, err := approvalScopeDriftReason(ctx, tx, tenantID, snapshot, false)
	if err != nil {
		return approvalScopeSnapshot{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return approvalScopeSnapshot{}, "", err
	}
	return snapshot, reason, nil
}

func sameApprovalScope(left, right approvalScopeSnapshot) bool {
	return left.status == right.status && left.kind == right.kind && left.runID == right.runID && left.toolID == right.toolID && left.proposalHash == right.proposalHash && left.permissionSnapshot == right.permissionSnapshot && left.userID == right.userID && left.version == right.version && left.targetVersion == right.targetVersion && left.updatedAt.Equal(right.updatedAt) && left.expiresAt.Equal(right.expiresAt)
}

func (service ApprovalControlService) loadInvalidatedApprovalReplay(ctx context.Context, tenantID, approvalID, eventID, storeEpoch string) (InvalidatedApproval, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return InvalidatedApproval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return InvalidatedApproval{}, err
	}
	var snapshot approvalScopeSnapshot
	err = tx.QueryRow(ctx, `SELECT status,version,approval_kind,COALESCE(run_id::text,''),COALESCE(tool_call_id::text,''),proposal_hash,target_version,permission_snapshot,requested_by::text,updated_at,expires_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2`, tenantID, approvalID).Scan(&snapshot.status, &snapshot.version, &snapshot.kind, &snapshot.runID, &snapshot.toolID, &snapshot.proposalHash, &snapshot.targetVersion, &snapshot.permissionSnapshot, &snapshot.userID, &snapshot.updatedAt, &snapshot.expiresAt)
	if err != nil || snapshot.status != "invalidated" {
		return InvalidatedApproval{}, ErrApprovalConflict
	}
	return service.commitInvalidatedApprovalReplay(ctx, tx, tenantID, approvalID, eventID, storeEpoch, snapshot)
}

func approvalScopeDriftReason(ctx context.Context, tx pgx.Tx, tenantID string, snapshot approvalScopeSnapshot, lock bool) (string, error) {
	currentPermission, err := loadPermissionSnapshot(ctx, tx, tenantID, snapshot.userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "permission_revoked", nil
	}
	if err != nil {
		return "", err
	}
	if currentPermission != snapshot.permissionSnapshot {
		return "permission_snapshot_changed", nil
	}
	var version uint64
	var status string
	if snapshot.kind == "tool_execution" {
		query := `SELECT tool_call_version,status FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2`
		if lock {
			query += ` FOR UPDATE`
		}
		err = tx.QueryRow(ctx, query, tenantID, snapshot.toolID).Scan(&version, &status)
		if err != nil {
			return "", err
		}
		if version != snapshot.targetVersion {
			return "target_version_changed", nil
		}
		if status != "awaiting_approval" {
			return "target_state_changed", nil
		}
		return "", nil
	}
	query := `SELECT run_version,status FROM agent.runs WHERE tenant_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	err = tx.QueryRow(ctx, query, tenantID, snapshot.runID).Scan(&version, &status)
	if err != nil {
		return "", err
	}
	if version != snapshot.targetVersion {
		return "target_version_changed", nil
	}
	if status != "waiting_approval" {
		return "target_state_changed", nil
	}
	return "", nil
}

func (service ApprovalControlService) commitInvalidatedApprovalReplay(ctx context.Context, tx pgx.Tx, tenantID, approvalID, eventID, storeEpoch string, snapshot approvalScopeSnapshot) (InvalidatedApproval, error) {
	var eventVersion uint64
	var invalidatedAt time.Time
	err := tx.QueryRow(ctx, `SELECT e.aggregate_version,a.invalidated_at FROM agent.events e JOIN agent.approvals a ON a.tenant_id=e.tenant_id AND a.id=e.aggregate_id WHERE e.tenant_id=$1 AND e.id=$2 AND e.event_type='ApprovalInvalidated' AND e.aggregate_kind='approval' AND e.aggregate_id=$3 AND e.store_epoch=$4`, tenantID, eventID, approvalID, storeEpoch).Scan(&eventVersion, &invalidatedAt)
	if err != nil {
		return InvalidatedApproval{}, err
	}
	if eventVersion != snapshot.version {
		return InvalidatedApproval{}, ErrApprovalConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return InvalidatedApproval{}, err
	}
	return InvalidatedApproval{ApprovalID: approvalID, Status: "invalidated", Version: snapshot.version, EventID: eventID, InvalidatedAt: invalidatedAt, Replayed: true}, nil
}
