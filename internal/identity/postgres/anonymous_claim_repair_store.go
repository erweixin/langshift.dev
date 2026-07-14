package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type proposeAnonymousClaimRepair struct {
	RepairID, TargetTenantID, SourceTenantID            string
	InitiatorUserID, InitiatorSessionID, TargetUserID   string
	ClaimID, ClaimKey                                   string
	ClaimVersion                                        uint64
	Resolution, ProposalHash, EvidenceHash, EvidenceRef string
	ExpiresAt                                           time.Time
	Actor                                               json.RawMessage
	CorrelationID                                       string
	Event                                               claimRepairPointer
	IDs                                                 claimRepairEventIDs
}

type proposedAnonymousClaimRepair struct {
	ID, Status string
	Version    uint64
	UpdatedAt  time.Time
}

type decideAnonymousClaimRepair struct {
	RepairID, TargetTenantID, ApproverUserID, SessionID string
	ApprovalID, Decision, ProposalHash                  string
	ExpectedRepairVersion, ExpectedClaimVersion         uint64
	PermissionSnapshot                                  string
	ReauthenticatedAt                                   time.Time
	Actor                                               json.RawMessage
	CorrelationID                                       string
	DecisionType, DecisionAggregateKind                 string
	DecisionAggregateID                                 string
	DecisionAggregateVersion                            uint64
	DecisionEvent, ApprovedEvent                        claimRepairPointer
	DecisionIDs, ApprovedIDs                            claimRepairEventIDs
}

type anonymousClaimRepairDecision struct {
	Status                 string
	Version, ApprovalCount uint64
	Ready                  bool
}

func (service AnonymousClaimRepairControlService) proposeAnonymousClaimRepair(ctx context.Context, command proposeAnonymousClaimRepair) (proposedAnonymousClaimRepair, error) {
	if err := service.requireCurrentEpoch(ctx); err != nil {
		return proposedAnonymousClaimRepair{}, err
	}
	now := service.now()
	command.ExpiresAt = command.ExpiresAt.UTC().Truncate(time.Microsecond)
	if command.RepairID == "" || command.TargetTenantID == "" || command.SourceTenantID != service.SystemTenantID || command.InitiatorUserID == "" || command.InitiatorSessionID == "" || command.TargetUserID == "" || command.ClaimID == "" || command.ClaimKey == "" || command.ClaimVersion == 0 || !isClaimRepairResolution(command.Resolution) || command.ProposalHash == "" || command.EvidenceHash == "" || command.EvidenceRef == "" || !command.ExpiresAt.After(now) || command.ExpiresAt.After(now.Add(24*time.Hour)) || !json.Valid(command.Actor) || command.CorrelationID == "" || command.Event.Ref == "" || command.Event.Hash == "" {
		return proposedAnonymousClaimRepair{}, errClaimRepairNotActionable
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return proposedAnonymousClaimRepair{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TargetTenantID); err != nil {
		return proposedAnonymousClaimRepair{}, err
	}
	var reauthenticatedAt time.Time
	var role string
	err = tx.QueryRow(ctx, `SELECT s.reauthenticated_at,m.role FROM identity.sessions s JOIN identity.memberships m ON m.tenant_id=s.active_tenant_id AND m.user_id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active_tenant_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$4 AND s.reauthenticated_at IS NOT NULL AND m.status='active'`, command.InitiatorSessionID, command.InitiatorUserID, command.TargetTenantID, now).Scan(&reauthenticatedAt, &role)
	if err != nil || (role != "owner" && role != "admin") || reauthenticatedAt.After(now) || now.Sub(reauthenticatedAt) > claimRepairReauthMaxAge {
		return proposedAnonymousClaimRepair{}, errClaimRepairAuthorization
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.SourceTenantID); err != nil {
		return proposedAnonymousClaimRepair{}, err
	}
	var status, claimKey, targetTenantID, targetUserID string
	var version uint64
	err = tx.QueryRow(ctx, `SELECT status,version,claim_key,target_tenant_id::text,target_user_id::text FROM identity.onboarding_claims WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.SourceTenantID, command.ClaimID).Scan(&status, &version, &claimKey, &targetTenantID, &targetUserID)
	if err != nil || status != "manual_review" || version != command.ClaimVersion || claimKey != command.ClaimKey || targetTenantID != command.TargetTenantID || targetUserID != command.TargetUserID {
		return proposedAnonymousClaimRepair{}, errClaimRepairNotActionable
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TargetTenantID); err != nil {
		return proposedAnonymousClaimRepair{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.repair_commands(id,tenant_id,source_tenant_id,anonymous_claim_id,repair_kind,target_kind,target_id,target_version,effect_version,effect_key,resolution,proposal_hash,evidence_hash,residual_risk_ref,initiator_user_id,status,expires_at,created_at,updated_at) VALUES($1,$2,$3,$4,'anonymous_claim_reconciliation','anonymous_claim',$4,$5,$5,$6,$7,$8,$9,NULL,$10,'proposed',$11,$12,$12) ON CONFLICT DO NOTHING`, command.RepairID, command.TargetTenantID, command.SourceTenantID, command.ClaimID, command.ClaimVersion, command.ClaimKey, command.Resolution, command.ProposalHash, command.EvidenceHash, command.InitiatorUserID, command.ExpiresAt, now)
	if err != nil {
		return proposedAnonymousClaimRepair{}, err
	}
	if tag.RowsAffected() == 0 {
		var actual proposedAnonymousClaimRepair
		var sourceTenantID, claimID, claimKey, resolution, proposalHash, evidenceHash, initiatorID string
		var claimVersion uint64
		err = tx.QueryRow(ctx, `SELECT id::text,status,version,updated_at,source_tenant_id::text,anonymous_claim_id::text,effect_key,resolution,proposal_hash,evidence_hash,initiator_user_id::text,target_version FROM agent.repair_commands WHERE tenant_id=$1 AND (id=$2 OR proposal_hash=$3) FOR UPDATE`, command.TargetTenantID, command.RepairID, command.ProposalHash).Scan(&actual.ID, &actual.Status, &actual.Version, &actual.UpdatedAt, &sourceTenantID, &claimID, &claimKey, &resolution, &proposalHash, &evidenceHash, &initiatorID, &claimVersion)
		if err != nil || actual.ID != command.RepairID || sourceTenantID != command.SourceTenantID || claimID != command.ClaimID || claimKey != command.ClaimKey || resolution != command.Resolution || proposalHash != command.ProposalHash || evidenceHash != command.EvidenceHash || initiatorID != command.InitiatorUserID || claimVersion != command.ClaimVersion {
			return proposedAnonymousClaimRepair{}, errClaimRepairConflict
		}
		var eventExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND aggregate_kind='repair_command' AND aggregate_id=$3 AND aggregate_version=1 AND event_type='RepairCommandProposed')`, command.TargetTenantID, command.IDs.event, command.RepairID).Scan(&eventExists); err != nil || !eventExists {
			return proposedAnonymousClaimRepair{}, errClaimRepairConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return proposedAnonymousClaimRepair{}, err
		}
		return actual, nil
	}
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: command.IDs.event, TenantID: command.TargetTenantID, UserID: command.TargetUserID, EventType: "RepairCommandProposed", SchemaVersion: 1, AggregateKind: "repair_command", AggregateID: command.RepairID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.Event.Ref, PayloadHash: command.Event.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: command.IDs.outbox, CommandID: command.IDs.publish, CommandType: "events.publish", PayloadRef: command.Event.Ref, PayloadHash: command.Event.Hash}}}
	if _, err = service.Appender.Append(ctx, tx, event); err != nil {
		return proposedAnonymousClaimRepair{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return proposedAnonymousClaimRepair{}, err
	}
	return proposedAnonymousClaimRepair{ID: command.RepairID, Status: "proposed", Version: 1, UpdatedAt: now}, nil
}

func (service AnonymousClaimRepairControlService) decideAnonymousClaimRepair(ctx context.Context, command decideAnonymousClaimRepair) (anonymousClaimRepairDecision, error) {
	if err := service.requireCurrentEpoch(ctx); err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TargetTenantID); err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	var sessionReauthenticated time.Time
	var role string
	err = tx.QueryRow(ctx, `SELECT s.reauthenticated_at,m.role FROM identity.sessions s JOIN identity.memberships m ON m.tenant_id=s.active_tenant_id AND m.user_id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active_tenant_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$4 AND s.reauthenticated_at IS NOT NULL AND m.status='active'`, command.SessionID, command.ApproverUserID, command.TargetTenantID, now).Scan(&sessionReauthenticated, &role)
	if err != nil || (role != "owner" && role != "admin") || sessionReauthenticated.After(now) || now.Sub(sessionReauthenticated) > claimRepairReauthMaxAge || !sessionReauthenticated.Equal(command.ReauthenticatedAt) {
		return anonymousClaimRepairDecision{}, errClaimRepairAuthorization
	}
	var status, proposalHash, initiatorID, sourceTenantID, claimID, claimKey, targetUserID string
	var repairVersion, claimVersion uint64
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT status,version,proposal_hash,initiator_user_id::text,source_tenant_id::text,anonymous_claim_id::text,effect_key,target_version,expires_at FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2 AND repair_kind='anonymous_claim_reconciliation' FOR UPDATE`, command.TargetTenantID, command.RepairID).Scan(&status, &repairVersion, &proposalHash, &initiatorID, &sourceTenantID, &claimID, &claimKey, &claimVersion, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return anonymousClaimRepairDecision{}, pgx.ErrNoRows
	}
	if err != nil || proposalHash != command.ProposalHash || claimVersion != command.ExpectedClaimVersion || initiatorID == command.ApproverUserID {
		return anonymousClaimRepairDecision{}, errClaimRepairNotActionable
	}
	var priorID, priorDecision, priorEventID, priorSessionID, priorProposalHash, priorPermission string
	var priorTargetVersion, priorEffectVersion uint64
	var priorReauthenticated time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,decision,approval_event_id::text,session_id::text,proposal_hash,target_version,effect_version,permission_snapshot,reauthenticated_at FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND approver_user_id=$3`, command.TargetTenantID, command.RepairID, command.ApproverUserID).Scan(&priorID, &priorDecision, &priorEventID, &priorSessionID, &priorProposalHash, &priorTargetVersion, &priorEffectVersion, &priorPermission, &priorReauthenticated)
	if err == nil {
		if priorID != command.ApprovalID || priorDecision != command.Decision || priorEventID != command.DecisionIDs.event || priorSessionID != command.SessionID || priorProposalHash != command.ProposalHash || priorTargetVersion != claimVersion || priorEffectVersion != claimVersion || priorPermission != command.PermissionSnapshot || !priorReauthenticated.Equal(command.ReauthenticatedAt) {
			return anonymousClaimRepairDecision{}, errClaimRepairConflict
		}
		var count uint64
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND decision='approve'`, command.TargetTenantID, command.RepairID).Scan(&count); err != nil {
			return anonymousClaimRepairDecision{}, err
		}
		var decisionEventExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type=$3 AND aggregate_kind=$4 AND aggregate_id=$5 AND aggregate_version=$6)`, command.TargetTenantID, command.DecisionIDs.event, command.DecisionType, command.DecisionAggregateKind, command.DecisionAggregateID, command.DecisionAggregateVersion).Scan(&decisionEventExists); err != nil || !decisionEventExists {
			return anonymousClaimRepairDecision{}, errClaimRepairConflict
		}
		if status == "approved" || status == "executed" {
			approvedVersion := repairVersion
			if status == "executed" {
				approvedVersion--
			}
			var approvedEventExists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='RepairCommandApproved' AND aggregate_kind='repair_command' AND aggregate_id=$3 AND aggregate_version=$4)`, command.TargetTenantID, command.ApprovedIDs.event, command.RepairID, approvedVersion).Scan(&approvedEventExists); err != nil || !approvedEventExists {
				return anonymousClaimRepairDecision{}, errClaimRepairConflict
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return anonymousClaimRepairDecision{}, err
		}
		return anonymousClaimRepairDecision{Status: status, Version: repairVersion, ApprovalCount: count, Ready: status == "approved" || status == "executed"}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) || status != "proposed" || repairVersion != command.ExpectedRepairVersion || !expiresAt.After(now) {
		return anonymousClaimRepairDecision{}, errClaimRepairNotActionable
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, sourceTenantID); err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	var claimStatus, lockedKey, lockedTargetTenant, lockedTargetUser string
	var lockedVersion uint64
	err = tx.QueryRow(ctx, `SELECT status,version,claim_key,target_tenant_id::text,target_user_id::text FROM identity.onboarding_claims WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, sourceTenantID, claimID).Scan(&claimStatus, &lockedVersion, &lockedKey, &lockedTargetTenant, &lockedTargetUser)
	if err != nil || claimStatus != "manual_review" || lockedVersion != claimVersion || lockedKey != claimKey || lockedTargetTenant != command.TargetTenantID {
		return anonymousClaimRepairDecision{}, errClaimRepairNotActionable
	}
	targetUserID = lockedTargetUser
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TargetTenantID); err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.repair_approvals(id,tenant_id,repair_command_id,approver_user_id,session_id,proposal_hash,target_version,effect_version,permission_snapshot,reauthenticated_at,approved_at,decision,approval_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8,$9,$10,$11,$12,$10,$10)`, command.ApprovalID, command.TargetTenantID, command.RepairID, command.ApproverUserID, command.SessionID, command.ProposalHash, claimVersion, command.PermissionSnapshot, command.ReauthenticatedAt, now, command.Decision, command.DecisionIDs.event); err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	if command.Decision == "reject" {
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.repair_commands SET version=version+1,status='rejected',rejected_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status='proposed'`, now, command.TargetTenantID, command.RepairID, repairVersion)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return anonymousClaimRepairDecision{}, errClaimRepairConflict
		}
	}
	decisionEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: command.DecisionIDs.event, TenantID: command.TargetTenantID, UserID: targetUserID, EventType: command.DecisionType, SchemaVersion: 1, AggregateKind: command.DecisionAggregateKind, AggregateID: command.DecisionAggregateID, AggregateVersion: command.DecisionAggregateVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.DecisionEvent.Ref, PayloadHash: command.DecisionEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: command.DecisionIDs.outbox, CommandID: command.DecisionIDs.publish, CommandType: "events.publish", PayloadRef: command.DecisionEvent.Ref, PayloadHash: command.DecisionEvent.Hash}}}
	if _, err = service.Appender.Append(ctx, tx, decisionEvent); err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	if command.Decision == "reject" {
		if err = tx.Commit(ctx); err != nil {
			return anonymousClaimRepairDecision{}, err
		}
		return anonymousClaimRepairDecision{Status: "rejected", Version: repairVersion + 1}, nil
	}
	var approvalCount uint64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND decision='approve'`, command.TargetTenantID, command.RepairID).Scan(&approvalCount); err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	result := anonymousClaimRepairDecision{Status: "proposed", Version: repairVersion, ApprovalCount: approvalCount}
	if approvalCount == 2 {
		if command.ApprovedEvent.Ref == "" || command.ApprovedEvent.Hash == "" {
			return anonymousClaimRepairDecision{}, errClaimRepairNotActionable
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.repair_commands SET version=version+1,status='approved',approved_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status='proposed' AND expires_at>$1`, now, command.TargetTenantID, command.RepairID, repairVersion)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return anonymousClaimRepairDecision{}, errClaimRepairConflict
		}
		causationID := command.DecisionIDs.event
		approvedEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: command.ApprovedIDs.event, TenantID: command.TargetTenantID, UserID: targetUserID, EventType: "RepairCommandApproved", SchemaVersion: 1, AggregateKind: "repair_command", AggregateID: command.RepairID, AggregateVersion: repairVersion + 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.ApprovedEvent.Ref, PayloadHash: command.ApprovedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: command.ApprovedIDs.outbox, CommandID: command.ApprovedIDs.publish, CommandType: "events.publish", PayloadRef: command.ApprovedEvent.Ref, PayloadHash: command.ApprovedEvent.Hash}}}
		if _, err = service.Appender.Append(ctx, tx, approvedEvent); err != nil {
			return anonymousClaimRepairDecision{}, err
		}
		result.Status, result.Version, result.Ready = "approved", repairVersion+1, true
	} else if approvalCount > 2 {
		return anonymousClaimRepairDecision{}, errClaimRepairConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return anonymousClaimRepairDecision{}, err
	}
	return result, nil
}
