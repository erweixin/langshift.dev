package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

const approvalReauthenticationMaxAge = 5 * time.Minute

var (
	ErrApprovalConflict      = errors.New("approval conflicts with durable state")
	ErrApprovalNotActionable = errors.New("approval is not actionable")
	ErrApprovalAuthorization = errors.New("approval decision is not authorized")
)

type RequestApprovalCommand struct {
	ApprovalID, TenantID, RunID, ToolCallID, ApprovalKind string
	ProposalHash, PermissionSnapshot                      string
	TargetVersion                                         uint64
	RequestedBy                                           string
	ExpiresAt                                             time.Time
	Actor                                                 json.RawMessage
	CorrelationID                                         string
	RequestedEvent                                        PayloadPointer
}

type RequestedApproval struct {
	ApprovalID, Status, ProposalHash, PermissionSnapshot string
	Version, TargetVersion                               uint64
	UpdatedAt                                            time.Time
	Replayed                                             bool
}

type DecideApprovalCommand struct {
	ApprovalID, TenantID, DecisionID, ActorUserID, SessionID string
	Decision, ProposalHash, PermissionSnapshot, Mode         string
	ExpectedApprovalVersion, TargetVersion                   uint64
	Actor                                                    json.RawMessage
	CorrelationID                                            string
	DecisionEvent                                            PayloadPointer
}

type DecidedApproval struct {
	ApprovalID, Status, Decision, EventID string
	Version                               uint64
	UpdatedAt                             time.Time
	Replayed                              bool
}

func (store RunStore) RequestApproval(ctx context.Context, command RequestApprovalCommand) (RequestedApproval, error) {
	if !store.valid() || !validApprovalRequest(command) {
		return RequestedApproval{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return RequestedApproval{}, err
	}
	now := store.claimNow()
	command.ExpiresAt = command.ExpiresAt.UTC().Truncate(time.Microsecond)
	idsForEvent, err := store.approvalEventIDs("requested", command.ApprovalID)
	if err != nil {
		return RequestedApproval{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RequestedApproval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return RequestedApproval{}, err
	}
	if replay, found, replayErr := store.loadRequestedApproval(ctx, tx, command, idsForEvent.event); replayErr != nil {
		return RequestedApproval{}, replayErr
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return RequestedApproval{}, err
		}
		return replay, nil
	}
	if !command.ExpiresAt.After(now) || command.ExpiresAt.After(now.Add(24*time.Hour)) {
		return RequestedApproval{}, ErrInvalidCommand
	}
	targetUserID, targetVersion, err := lockApprovalTarget(ctx, tx, command.TenantID, command.RunID, command.ToolCallID, command.ApprovalKind)
	if err != nil {
		if errors.Is(err, ErrApprovalNotActionable) || errors.Is(err, pgx.ErrNoRows) {
			return RequestedApproval{}, ErrApprovalNotActionable
		}
		return RequestedApproval{}, err
	}
	if targetVersion != command.TargetVersion || targetUserID != command.RequestedBy {
		return RequestedApproval{}, ErrApprovalNotActionable
	}
	currentPermission, err := loadPermissionSnapshot(ctx, tx, command.TenantID, targetUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestedApproval{}, ErrApprovalNotActionable
		}
		return RequestedApproval{}, err
	}
	if currentPermission != command.PermissionSnapshot {
		return RequestedApproval{}, ErrApprovalNotActionable
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.approvals(id,tenant_id,run_id,tool_call_id,approval_kind,proposal_hash,target_version,permission_snapshot,status,expires_at,requested_by,created_at,updated_at) VALUES($1,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,$5,$6,$7,$8,'pending',$9,$10,$11,$11) ON CONFLICT DO NOTHING`, command.ApprovalID, command.TenantID, command.RunID, command.ToolCallID, command.ApprovalKind, command.ProposalHash, command.TargetVersion, command.PermissionSnapshot, command.ExpiresAt, command.RequestedBy, now)
	if err != nil {
		return RequestedApproval{}, err
	}
	if tag.RowsAffected() == 0 {
		actual, found, replayErr := store.loadRequestedApproval(ctx, tx, command, idsForEvent.event)
		if replayErr != nil || !found {
			return RequestedApproval{}, ErrApprovalConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return RequestedApproval{}, err
		}
		return actual, nil
	}
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: idsForEvent.event, TenantID: command.TenantID, UserID: targetUserID, EventType: "ApprovalRequested", SchemaVersion: 1, AggregateKind: "approval", AggregateID: command.ApprovalID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.RequestedEvent.Ref, PayloadHash: command.RequestedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: idsForEvent.outbox, CommandID: idsForEvent.publish, CommandType: "events.publish", PayloadRef: command.RequestedEvent.Ref, PayloadHash: command.RequestedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return RequestedApproval{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RequestedApproval{}, err
	}
	return RequestedApproval{ApprovalID: command.ApprovalID, Status: "pending", ProposalHash: command.ProposalHash, PermissionSnapshot: command.PermissionSnapshot, Version: 1, TargetVersion: command.TargetVersion, UpdatedAt: now}, nil
}

func (store RunStore) DecideApproval(ctx context.Context, command DecideApprovalCommand) (DecidedApproval, error) {
	if !store.valid() || !validApprovalDecision(command) {
		return DecidedApproval{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return DecidedApproval{}, err
	}
	now := store.claimNow()
	idsForEvent, err := store.approvalEventIDs("decision", command.DecisionID)
	if err != nil {
		return DecidedApproval{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return DecidedApproval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return DecidedApproval{}, err
	}
	nextStatus, eventType, timestampColumn := approvalDecisionOutcome(command.Decision)
	var status, proposalHash, permissionSnapshot, runID, toolID, kind string
	var version, targetVersion uint64
	var expiresAt, updatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT status,version,proposal_hash,permission_snapshot,target_version,COALESCE(run_id::text,''),COALESCE(tool_call_id::text,''),approval_kind,expires_at,updated_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ApprovalID).Scan(&status, &version, &proposalHash, &permissionSnapshot, &targetVersion, &runID, &toolID, &kind, &expiresAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DecidedApproval{}, ErrApprovalNotActionable
		}
		return DecidedApproval{}, err
	}
	if proposalHash != command.ProposalHash || permissionSnapshot != command.PermissionSnapshot || targetVersion != command.TargetVersion {
		return DecidedApproval{}, ErrApprovalNotActionable
	}
	var priorID, priorSession, priorDecision, priorMode, priorProposal, priorPermission, priorEvent string
	var priorTargetVersion uint64
	err = tx.QueryRow(ctx, `SELECT id::text,session_id::text,decision,decision_mode,proposal_hash,target_version,permission_snapshot,decision_event_id::text FROM agent.approval_decisions WHERE tenant_id=$1 AND approval_id=$2 AND actor_user_id=$3`, command.TenantID, command.ApprovalID, command.ActorUserID).Scan(&priorID, &priorSession, &priorDecision, &priorMode, &priorProposal, &priorTargetVersion, &priorPermission, &priorEvent)
	if err == nil {
		if priorID != command.DecisionID || priorSession != command.SessionID || priorDecision != command.Decision || priorMode != command.Mode || priorProposal != command.ProposalHash || priorTargetVersion != command.TargetVersion || priorPermission != command.PermissionSnapshot || priorEvent != idsForEvent.event || version != command.ExpectedApprovalVersion+1 || status != nextStatus {
			return DecidedApproval{}, ErrApprovalConflict
		}
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type=$3 AND aggregate_kind='approval' AND aggregate_id=$4 AND aggregate_version=$5 AND store_epoch=$6)`, command.TenantID, idsForEvent.event, eventType, command.ApprovalID, version, store.StoreEpoch).Scan(&exists); err != nil || !exists {
			return DecidedApproval{}, ErrApprovalConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return DecidedApproval{}, err
		}
		return DecidedApproval{ApprovalID: command.ApprovalID, Status: status, Decision: command.Decision, EventID: idsForEvent.event, Version: version, UpdatedAt: updatedAt, Replayed: true}, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DecidedApproval{}, err
	}
	if status != "pending" || version != command.ExpectedApprovalVersion || !expiresAt.After(now) {
		return DecidedApproval{}, ErrApprovalNotActionable
	}
	targetUserID, currentTargetVersion, err := lockApprovalTarget(ctx, tx, command.TenantID, runID, toolID, kind)
	if err != nil {
		if errors.Is(err, ErrApprovalNotActionable) || errors.Is(err, pgx.ErrNoRows) {
			return DecidedApproval{}, ErrApprovalNotActionable
		}
		return DecidedApproval{}, err
	}
	if currentTargetVersion != targetVersion {
		return DecidedApproval{}, ErrApprovalNotActionable
	}
	currentPermission, err := loadPermissionSnapshot(ctx, tx, command.TenantID, targetUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DecidedApproval{}, ErrApprovalNotActionable
		}
		return DecidedApproval{}, err
	}
	if currentPermission != permissionSnapshot {
		return DecidedApproval{}, ErrApprovalNotActionable
	}
	var reauthenticatedAt time.Time
	var actorRole string
	err = tx.QueryRow(ctx, `SELECT s.reauthenticated_at,m.role FROM identity.sessions s JOIN identity.memberships m ON m.tenant_id=s.active_tenant_id AND m.user_id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active_tenant_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$4 AND s.reauthenticated_at IS NOT NULL AND m.status='active'`, command.SessionID, command.ActorUserID, command.TenantID, now).Scan(&reauthenticatedAt, &actorRole)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DecidedApproval{}, ErrApprovalAuthorization
		}
		return DecidedApproval{}, err
	}
	if reauthenticatedAt.After(now) || now.Sub(reauthenticatedAt) > approvalReauthenticationMaxAge || (command.Mode == "user" && command.ActorUserID != targetUserID) || (command.Mode == "admin" && actorRole != "owner" && actorRole != "admin") {
		return DecidedApproval{}, ErrApprovalAuthorization
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.approval_decisions(id,tenant_id,approval_id,actor_user_id,session_id,decision,decision_mode,proposal_hash,target_version,permission_snapshot,reauthenticated_at,decided_at,decision_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$12,$12)`, command.DecisionID, command.TenantID, command.ApprovalID, command.ActorUserID, command.SessionID, command.Decision, command.Mode, proposalHash, targetVersion, permissionSnapshot, reauthenticatedAt, now, idsForEvent.event); err != nil {
		return DecidedApproval{}, err
	}
	update := fmt.Sprintf(`UPDATE agent.approvals SET version=version+1,status=$1,%s=$2,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='pending' AND expires_at>$2`, timestampColumn)
	tag, err := tx.Exec(ctx, update, nextStatus, now, command.TenantID, command.ApprovalID, version)
	if err != nil || tag.RowsAffected() != 1 {
		return DecidedApproval{}, ErrApprovalConflict
	}
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: idsForEvent.event, TenantID: command.TenantID, UserID: targetUserID, EventType: eventType, SchemaVersion: 1, AggregateKind: "approval", AggregateID: command.ApprovalID, AggregateVersion: version + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.DecisionEvent.Ref, PayloadHash: command.DecisionEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: idsForEvent.outbox, CommandID: idsForEvent.publish, CommandType: "events.publish", PayloadRef: command.DecisionEvent.Ref, PayloadHash: command.DecisionEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return DecidedApproval{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DecidedApproval{}, err
	}
	return DecidedApproval{ApprovalID: command.ApprovalID, Status: nextStatus, Decision: command.Decision, EventID: idsForEvent.event, Version: version + 1, UpdatedAt: now}, nil
}

type approvalIDs struct{ event, outbox, publish string }

func (store RunStore) loadRequestedApproval(ctx context.Context, tx pgx.Tx, command RequestApprovalCommand, eventID string) (RequestedApproval, bool, error) {
	var actual RequestedApproval
	var runID, toolID, kind, requestedBy string
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `SELECT id::text,status,version,proposal_hash,permission_snapshot,target_version,updated_at,COALESCE(run_id::text,''),COALESCE(tool_call_id::text,''),approval_kind,requested_by::text,expires_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ApprovalID).Scan(&actual.ApprovalID, &actual.Status, &actual.Version, &actual.ProposalHash, &actual.PermissionSnapshot, &actual.TargetVersion, &actual.UpdatedAt, &runID, &toolID, &kind, &requestedBy, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestedApproval{}, false, nil
	}
	if err != nil {
		return RequestedApproval{}, false, err
	}
	if actual.ProposalHash != command.ProposalHash || actual.PermissionSnapshot != command.PermissionSnapshot || actual.TargetVersion != command.TargetVersion || runID != command.RunID || toolID != command.ToolCallID || kind != command.ApprovalKind || requestedBy != command.RequestedBy || !expiresAt.Equal(command.ExpiresAt) {
		return RequestedApproval{}, false, ErrApprovalConflict
	}
	var eventExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='ApprovalRequested' AND aggregate_kind='approval' AND aggregate_id=$3 AND aggregate_version=1 AND store_epoch=$4)`, command.TenantID, eventID, command.ApprovalID, store.StoreEpoch).Scan(&eventExists); err != nil {
		return RequestedApproval{}, false, err
	}
	if !eventExists {
		return RequestedApproval{}, false, ErrApprovalConflict
	}
	actual.Replayed = true
	return actual, true, nil
}

func approvalDecisionOutcome(decision string) (status, eventType, timestampColumn string) {
	switch decision {
	case "reject":
		return "rejected", "ApprovalRejected", "rejected_at"
	case "revise":
		return "invalidated", "ApprovalInvalidated", "invalidated_at"
	default:
		return "granted", "ApprovalGranted", "granted_at"
	}
}

func (store RunStore) approvalEventIDs(kind, seed string) (approvalIDs, error) {
	derive := func(part string) (string, error) {
		return ids.DeterministicUUID(store.IDKey, "approval:"+kind+":"+part, seed)
	}
	eventID, err := derive("event")
	if err != nil {
		return approvalIDs{}, err
	}
	outboxID, err := derive("outbox")
	if err != nil {
		return approvalIDs{}, err
	}
	publishID, err := derive("publish")
	return approvalIDs{eventID, outboxID, publishID}, err
}

func lockApprovalTarget(ctx context.Context, tx pgx.Tx, tenantID, runID, toolID, kind string) (string, uint64, error) {
	var userID string
	var version uint64
	if kind == "tool_execution" {
		var status string
		err := tx.QueryRow(ctx, `SELECT user_id::text,tool_call_version,status FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, toolID).Scan(&userID, &version, &status)
		if err != nil {
			return "", 0, err
		}
		if status != "awaiting_approval" {
			return "", 0, ErrApprovalNotActionable
		}
		return userID, version, nil
	}
	var status string
	err := tx.QueryRow(ctx, `SELECT user_id::text,run_version,status FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, runID).Scan(&userID, &version, &status)
	if err != nil {
		return "", 0, err
	}
	if status != "waiting_approval" {
		return "", 0, ErrApprovalNotActionable
	}
	return userID, version, nil
}

func loadPermissionSnapshot(ctx context.Context, tx pgx.Tx, tenantID, userID string) (string, error) {
	var membershipID, role string
	var version uint64
	err := tx.QueryRow(ctx, `SELECT id::text,version,role FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2 AND status='active'`, tenantID, userID).Scan(&membershipID, &version, &role)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("membership:%s:v%d:role:%s", membershipID, version, role), nil
}

func validApprovalRequest(c RequestApprovalCommand) bool {
	target := c.ApprovalKind == "tool_execution" && c.RunID != "" && c.ToolCallID != "" || c.ApprovalKind == "stage_checkpoint" && c.RunID != "" && c.ToolCallID == ""
	return c.ApprovalID != "" && c.TenantID != "" && target && c.ProposalHash != "" && c.TargetVersion > 0 && c.PermissionSnapshot != "" && c.RequestedBy != "" && !c.ExpiresAt.IsZero() && validJSONObject(c.Actor) && c.CorrelationID != "" && validPointer(c.RequestedEvent)
}

func validApprovalDecision(c DecideApprovalCommand) bool {
	decision := c.Decision == "approve" || c.Decision == "reject" || c.Decision == "revise"
	mode := c.Mode == "user" || c.Mode == "admin" && c.Decision != "revise"
	return c.ApprovalID != "" && c.TenantID != "" && c.DecisionID != "" && c.ActorUserID != "" && c.SessionID != "" && decision && mode && c.ProposalHash != "" && c.PermissionSnapshot != "" && c.ExpectedApprovalVersion > 0 && c.TargetVersion > 0 && validJSONObject(c.Actor) && c.CorrelationID != "" && validPointer(c.DecisionEvent)
}
