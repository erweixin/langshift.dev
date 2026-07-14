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

const repairReauthenticationMaxAge = 5 * time.Minute

var (
	ErrRepairConflict      = errors.New("repair command conflicts with durable state")
	ErrRepairNotActionable = errors.New("repair command is not actionable")
	ErrRepairAuthorization = errors.New("repair decision is not authorized")
)

type ProposeToolEffectRepairCommand struct {
	RepairID, TenantID, InitiatorUserID, InitiatorSessionID, ToolCallID string
	ExpectedToolVersion, ExpectedEffectVersion                          uint64
	EffectKey, Resolution, ProposalHash, EvidenceHash                   string
	ResidualRiskRef                                                     string
	ExpiresAt                                                           time.Time
	Actor                                                               json.RawMessage
	CorrelationID                                                       string
	ProposedEvent                                                       PayloadPointer
}

type ProposedRepair struct {
	RepairID, Status, Resolution, ProposalHash string
	Version                                    uint64
	Replayed                                   bool
}

type DecideRepairCommand struct {
	RepairID, TenantID, ApproverUserID, SessionID string
	ApprovalID, Decision, ProposalHash            string
	ExpectedRepairVersion, ExpectedToolVersion    uint64
	ExpectedEffectVersion                         uint64
	PermissionSnapshot                            string
	ReauthenticatedAt                             time.Time
	Actor                                         json.RawMessage
	CorrelationID                                 string
	DecisionEvent, RepairApprovedEvent            PayloadPointer
}

type RepairDecision struct {
	RepairID, ApprovalID, Status string
	Version                      uint64
	ApprovalCount                int
	Ready                        bool
}

func (store RunStore) ProposeToolEffectRepair(ctx context.Context, command ProposeToolEffectRepairCommand) (ProposedRepair, error) {
	if !store.valid() || !validRepairProposal(command) {
		return ProposedRepair{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return ProposedRepair{}, err
	}
	now := store.claimNow()
	command.ExpiresAt = command.ExpiresAt.UTC().Truncate(time.Microsecond)
	if !command.ExpiresAt.After(now) || command.ExpiresAt.After(now.Add(24*time.Hour)) {
		return ProposedRepair{}, ErrInvalidCommand
	}
	eventID, eventOutboxID, eventPublishID, err := repairProposalEventIDs(store.IDKey, command.RepairID)
	if err != nil {
		return ProposedRepair{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ProposedRepair{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ProposedRepair{}, err
	}
	var initiatorReauthenticated time.Time
	var initiatorRole string
	if err = tx.QueryRow(ctx, `SELECT s.reauthenticated_at,m.role FROM identity.sessions s JOIN identity.memberships m ON m.tenant_id=s.active_tenant_id AND m.user_id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active_tenant_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$4 AND s.reauthenticated_at IS NOT NULL AND m.status='active'`, command.InitiatorSessionID, command.InitiatorUserID, command.TenantID, now).Scan(&initiatorReauthenticated, &initiatorRole); err != nil || (initiatorRole != "owner" && initiatorRole != "admin") || initiatorReauthenticated.After(now) || now.Sub(initiatorReauthenticated) > repairReauthenticationMaxAge {
		return ProposedRepair{}, ErrRepairAuthorization
	}
	if replay, found, replayErr := loadRepairProposalReplay(ctx, tx, command, eventID); replayErr != nil {
		return ProposedRepair{}, replayErr
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return ProposedRepair{}, err
		}
		return replay, nil
	}
	var targetUserID, toolStatus, effectStatus, effectKey string
	var toolVersion, effectVersion uint64
	err = tx.QueryRow(ctx, `SELECT t.user_id::text,t.status,t.tool_call_version,e.status,e.version,e.effect_key FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE t.tenant_id=$1 AND t.id=$2 FOR UPDATE OF t,e`, command.TenantID, command.ToolCallID).Scan(&targetUserID, &toolStatus, &toolVersion, &effectStatus, &effectVersion, &effectKey)
	if err != nil || toolStatus != "outcome_unknown" || effectStatus != "outcome_unknown" || toolVersion != command.ExpectedToolVersion || effectVersion != command.ExpectedEffectVersion || effectKey != command.EffectKey {
		return ProposedRepair{}, ErrRepairNotActionable
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.repair_commands(id,tenant_id,source_tenant_id,tool_call_id,repair_kind,target_kind,target_id,target_version,effect_version,effect_key,resolution,proposal_hash,evidence_hash,residual_risk_ref,initiator_user_id,status,expires_at,created_at,updated_at) VALUES($1,$2,$2,$3,'tool_effect_resolution','tool_call',$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,'proposed',$12,$13,$13) ON CONFLICT DO NOTHING`, command.RepairID, command.TenantID, command.ToolCallID, command.ExpectedToolVersion, command.ExpectedEffectVersion, command.EffectKey, command.Resolution, command.ProposalHash, command.EvidenceHash, command.ResidualRiskRef, command.InitiatorUserID, command.ExpiresAt, now)
	if err != nil {
		return ProposedRepair{}, err
	}
	if tag.RowsAffected() == 0 {
		replay, found, replayErr := loadRepairProposalReplay(ctx, tx, command, eventID)
		if replayErr != nil || !found {
			return ProposedRepair{}, ErrRepairConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return ProposedRepair{}, err
		}
		return replay, nil
	}
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: targetUserID, EventType: "RepairCommandProposed", SchemaVersion: 1, AggregateKind: "repair_command", AggregateID: command.RepairID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.ProposedEvent.Ref, PayloadHash: command.ProposedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventOutboxID, CommandID: eventPublishID, CommandType: "events.publish", PayloadRef: command.ProposedEvent.Ref, PayloadHash: command.ProposedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return ProposedRepair{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProposedRepair{}, err
	}
	return ProposedRepair{RepairID: command.RepairID, Status: "proposed", Resolution: command.Resolution, ProposalHash: command.ProposalHash, Version: 1}, nil
}

func loadRepairProposalReplay(ctx context.Context, tx pgx.Tx, command ProposeToolEffectRepairCommand, eventID string) (ProposedRepair, bool, error) {
	var actual ProposedRepair
	var targetID, initiatorID, effectKey, evidenceHash, residualRisk string
	var targetVersion, effectVersion uint64
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `SELECT id::text,status,version,resolution,proposal_hash,target_id::text,target_version,effect_version,effect_key,evidence_hash,COALESCE(residual_risk_ref,''),initiator_user_id::text,expires_at FROM agent.repair_commands WHERE tenant_id=$1 AND (id=$2 OR proposal_hash=$3) FOR UPDATE`, command.TenantID, command.RepairID, command.ProposalHash).Scan(&actual.RepairID, &actual.Status, &actual.Version, &actual.Resolution, &actual.ProposalHash, &targetID, &targetVersion, &effectVersion, &effectKey, &evidenceHash, &residualRisk, &initiatorID, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProposedRepair{}, false, nil
	}
	if err != nil {
		return ProposedRepair{}, false, err
	}
	if actual.RepairID != command.RepairID || actual.Resolution != command.Resolution || actual.ProposalHash != command.ProposalHash || targetID != command.ToolCallID || targetVersion != command.ExpectedToolVersion || effectVersion != command.ExpectedEffectVersion || effectKey != command.EffectKey || evidenceHash != command.EvidenceHash || residualRisk != command.ResidualRiskRef || initiatorID != command.InitiatorUserID || !expiresAt.Equal(command.ExpiresAt) {
		return ProposedRepair{}, true, ErrRepairConflict
	}
	var eventExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE id=$1 AND tenant_id=$2 AND aggregate_kind='repair_command' AND aggregate_id=$3 AND aggregate_version=1 AND event_type='RepairCommandProposed')`, eventID, command.TenantID, command.RepairID).Scan(&eventExists); err != nil || !eventExists {
		return ProposedRepair{}, true, ErrRepairConflict
	}
	actual.Replayed = true
	return actual, true, nil
}

func (store RunStore) DecideToolEffectRepair(ctx context.Context, command DecideRepairCommand) (RepairDecision, error) {
	if !store.valid() || !validRepairDecision(command) {
		return RepairDecision{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return RepairDecision{}, err
	}
	now := store.claimNow()
	if !command.ReauthenticatedAt.IsZero() {
		command.ReauthenticatedAt = command.ReauthenticatedAt.UTC().Truncate(time.Microsecond)
	}
	if !command.ReauthenticatedAt.IsZero() && (command.ReauthenticatedAt.After(now) || now.Sub(command.ReauthenticatedAt) > repairReauthenticationMaxAge) {
		return RepairDecision{}, ErrRepairAuthorization
	}
	decisionEventID, decisionOutboxID, decisionPublishID, err := repairDecisionEventIDs(store.IDKey, command.ApprovalID)
	if err != nil {
		return RepairDecision{}, err
	}
	approvedEventID, approvedOutboxID, approvedPublishID, err := repairApprovedEventIDs(store.IDKey, command.RepairID)
	if err != nil {
		return RepairDecision{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RepairDecision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return RepairDecision{}, err
	}
	var sessionReauthenticated time.Time
	var role string
	err = tx.QueryRow(ctx, `SELECT s.reauthenticated_at,m.role FROM identity.sessions s JOIN identity.memberships m ON m.tenant_id=s.active_tenant_id AND m.user_id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active_tenant_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$4 AND s.reauthenticated_at IS NOT NULL AND m.status='active'`, command.SessionID, command.ApproverUserID, command.TenantID, now).Scan(&sessionReauthenticated, &role)
	if err != nil || sessionReauthenticated.After(now) || now.Sub(sessionReauthenticated) > repairReauthenticationMaxAge || (!command.ReauthenticatedAt.IsZero() && !sessionReauthenticated.Equal(command.ReauthenticatedAt)) || (role != "owner" && role != "admin") {
		return RepairDecision{}, ErrRepairAuthorization
	}
	command.ReauthenticatedAt = sessionReauthenticated
	var status, proposalHash, initiatorID, targetID, effectKey, targetUserID string
	var repairVersion, targetVersion, effectVersion uint64
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT r.status,r.version,r.proposal_hash,r.initiator_user_id::text,r.target_id::text,r.target_version,r.effect_version,r.effect_key,r.expires_at,t.user_id::text FROM agent.repair_commands r JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.id=r.target_id WHERE r.id=$1 AND r.tenant_id=$2 FOR UPDATE OF r,t`, command.RepairID, command.TenantID).Scan(&status, &repairVersion, &proposalHash, &initiatorID, &targetID, &targetVersion, &effectVersion, &effectKey, &expiresAt, &targetUserID)
	if err != nil || proposalHash != command.ProposalHash || targetVersion != command.ExpectedToolVersion || effectVersion != command.ExpectedEffectVersion || initiatorID == command.ApproverUserID {
		return RepairDecision{}, ErrRepairNotActionable
	}
	var priorID, priorDecision, priorEventID, priorSessionID, priorProposalHash, priorPermission string
	var priorTargetVersion, priorEffectVersion uint64
	var priorReauthenticated time.Time
	err = tx.QueryRow(ctx, `SELECT id::text,decision,approval_event_id::text,session_id::text,proposal_hash,target_version,effect_version,permission_snapshot,reauthenticated_at FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND approver_user_id=$3`, command.TenantID, command.RepairID, command.ApproverUserID).Scan(&priorID, &priorDecision, &priorEventID, &priorSessionID, &priorProposalHash, &priorTargetVersion, &priorEffectVersion, &priorPermission, &priorReauthenticated)
	if err == nil {
		if priorID != command.ApprovalID || priorDecision != command.Decision || priorEventID != decisionEventID || priorSessionID != command.SessionID || priorProposalHash != command.ProposalHash || priorTargetVersion != command.ExpectedToolVersion || priorEffectVersion != command.ExpectedEffectVersion || priorPermission != command.PermissionSnapshot || !priorReauthenticated.Equal(command.ReauthenticatedAt) {
			return RepairDecision{}, ErrRepairConflict
		}
		aggregateKind, aggregateID, aggregateVersion := "repair_approval", command.ApprovalID, uint64(1)
		if priorDecision == "reject" {
			aggregateKind, aggregateID, aggregateVersion = "repair_command", command.RepairID, command.ExpectedRepairVersion+1
		}
		var eventExists bool
		decisionType := "ApprovalGranted"
		if priorDecision == "reject" {
			decisionType = "ApprovalRejected"
		}
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE id=$1 AND tenant_id=$2 AND aggregate_kind=$3 AND aggregate_id=$4 AND aggregate_version=$5 AND event_type=$6)`, priorEventID, command.TenantID, aggregateKind, aggregateID, aggregateVersion, decisionType).Scan(&eventExists); err != nil || !eventExists {
			return RepairDecision{}, ErrRepairConflict
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND decision='approve'`, command.TenantID, command.RepairID).Scan(&count); err != nil {
			return RepairDecision{}, err
		}
		if status == "approved" || status == "executed" {
			approvedVersion := repairVersion
			if status == "executed" {
				approvedVersion--
			}
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE id=$1 AND tenant_id=$2 AND aggregate_kind='repair_command' AND aggregate_id=$3 AND aggregate_version=$4 AND event_type='RepairCommandApproved')`, approvedEventID, command.TenantID, command.RepairID, approvedVersion).Scan(&eventExists); err != nil || !eventExists {
				return RepairDecision{}, ErrRepairConflict
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return RepairDecision{}, err
		}
		return RepairDecision{RepairID: command.RepairID, ApprovalID: command.ApprovalID, Status: status, Version: repairVersion, ApprovalCount: count, Ready: status == "approved" || status == "executed"}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RepairDecision{}, err
	}
	if status != "proposed" || repairVersion != command.ExpectedRepairVersion || !expiresAt.After(now) {
		return RepairDecision{}, ErrRepairNotActionable
	}
	var toolStatus, effectStatus, lockedEffectKey string
	var lockedToolVersion, lockedEffectVersion uint64
	err = tx.QueryRow(ctx, `SELECT t.status,t.tool_call_version,e.status,e.version,e.effect_key FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE t.tenant_id=$1 AND t.id=$2 FOR UPDATE OF t,e`, command.TenantID, targetID).Scan(&toolStatus, &lockedToolVersion, &effectStatus, &lockedEffectVersion, &lockedEffectKey)
	if err != nil || toolStatus != "outcome_unknown" || effectStatus != "outcome_unknown" || lockedToolVersion != targetVersion || lockedEffectVersion != effectVersion || lockedEffectKey != effectKey {
		return RepairDecision{}, ErrRepairNotActionable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.repair_approvals(id,tenant_id,repair_command_id,approver_user_id,session_id,proposal_hash,target_version,effect_version,permission_snapshot,reauthenticated_at,approved_at,decision,approval_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$11,$11)`, command.ApprovalID, command.TenantID, command.RepairID, command.ApproverUserID, command.SessionID, command.ProposalHash, targetVersion, effectVersion, command.PermissionSnapshot, command.ReauthenticatedAt, now, command.Decision, decisionEventID); err != nil {
		return RepairDecision{}, err
	}
	decisionType := "ApprovalGranted"
	if command.Decision == "reject" {
		decisionType = "ApprovalRejected"
	}
	aggregateKind, aggregateID, aggregateVersion := "repair_approval", command.ApprovalID, uint64(1)
	if command.Decision == "reject" {
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.repair_commands SET version=version+1,status='rejected',rejected_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND status='proposed'`, now, command.RepairID, command.TenantID, repairVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return RepairDecision{}, ErrRepairConflict
		}
		aggregateKind, aggregateID, aggregateVersion = "repair_command", command.RepairID, repairVersion+1
	}
	decisionEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: decisionEventID, TenantID: command.TenantID, UserID: targetUserID, EventType: decisionType, SchemaVersion: 1, AggregateKind: aggregateKind, AggregateID: aggregateID, AggregateVersion: aggregateVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.DecisionEvent.Ref, PayloadHash: command.DecisionEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: decisionOutboxID, CommandID: decisionPublishID, CommandType: "events.publish", PayloadRef: command.DecisionEvent.Ref, PayloadHash: command.DecisionEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, decisionEvent); err != nil {
		return RepairDecision{}, err
	}
	if command.Decision == "reject" {
		if err = tx.Commit(ctx); err != nil {
			return RepairDecision{}, err
		}
		return RepairDecision{RepairID: command.RepairID, ApprovalID: command.ApprovalID, Status: "rejected", Version: repairVersion + 1}, nil
	}
	var approvalCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND decision='approve'`, command.TenantID, command.RepairID).Scan(&approvalCount); err != nil {
		return RepairDecision{}, err
	}
	result := RepairDecision{RepairID: command.RepairID, ApprovalID: command.ApprovalID, Status: "proposed", Version: repairVersion, ApprovalCount: approvalCount}
	if approvalCount == 2 {
		if !validPointer(command.RepairApprovedEvent) {
			return RepairDecision{}, ErrInvalidCommand
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.repair_commands SET version=version+1,status='approved',approved_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND status='proposed' AND expires_at>$1`, now, command.RepairID, command.TenantID, repairVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return RepairDecision{}, ErrRepairConflict
		}
		causationID := decisionEventID
		approvedEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: approvedEventID, TenantID: command.TenantID, UserID: targetUserID, EventType: "RepairCommandApproved", SchemaVersion: 1, AggregateKind: "repair_command", AggregateID: command.RepairID, AggregateVersion: repairVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.RepairApprovedEvent.Ref, PayloadHash: command.RepairApprovedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: approvedOutboxID, CommandID: approvedPublishID, CommandType: "events.publish", PayloadRef: command.RepairApprovedEvent.Ref, PayloadHash: command.RepairApprovedEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, approvedEvent); err != nil {
			return RepairDecision{}, err
		}
		result.Status, result.Version, result.Ready = "approved", repairVersion+1, true
	}
	if err = tx.Commit(ctx); err != nil {
		return RepairDecision{}, err
	}
	return result, nil
}

func validRepairProposal(command ProposeToolEffectRepairCommand) bool {
	resolutionValid := command.Resolution == "confirmed_occurred" || command.Resolution == "confirmed_not_occurred" || command.Resolution == "accepted_unknown"
	riskValid := command.Resolution == "accepted_unknown" && command.ResidualRiskRef != "" || command.Resolution != "accepted_unknown" && command.ResidualRiskRef == ""
	return command.RepairID != "" && command.TenantID != "" && command.InitiatorUserID != "" && command.InitiatorSessionID != "" && command.ToolCallID != "" && command.ExpectedToolVersion > 0 && command.ExpectedEffectVersion > 0 && command.EffectKey != "" && resolutionValid && riskValid && command.ProposalHash != "" && command.EvidenceHash != "" && !command.ExpiresAt.IsZero() && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ProposedEvent)
}

func validRepairDecision(command DecideRepairCommand) bool {
	approvedPointerValid := command.RepairApprovedEvent == (PayloadPointer{}) || validPointer(command.RepairApprovedEvent)
	return command.RepairID != "" && command.TenantID != "" && command.ApproverUserID != "" && command.SessionID != "" && command.ApprovalID != "" && (command.Decision == "approve" || command.Decision == "reject") && command.ProposalHash != "" && command.ExpectedRepairVersion > 0 && command.ExpectedToolVersion > 0 && command.ExpectedEffectVersion > 0 && command.PermissionSnapshot != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.DecisionEvent) && approvedPointerValid
}

func repairProposalEventIDs(key []byte, repairID string) (string, string, string, error) {
	return threeRepairIDs(key, repairID, "repair-proposed-event", "repair-proposed-outbox", "repair-proposed-publish")
}

func repairDecisionEventIDs(key []byte, approvalID string) (string, string, string, error) {
	return threeRepairIDs(key, approvalID, "repair-decision-event", "repair-decision-outbox", "repair-decision-publish")
}

func repairApprovedEventIDs(key []byte, repairID string) (string, string, string, error) {
	return threeRepairIDs(key, repairID, "repair-approved-event", "repair-approved-outbox", "repair-approved-publish")
}

func threeRepairIDs(key []byte, seed, eventDomain, outboxDomain, publishDomain string) (string, string, string, error) {
	eventID, err := ids.DeterministicUUID(key, eventDomain, seed)
	if err != nil {
		return "", "", "", err
	}
	outboxID, err := ids.DeterministicUUID(key, outboxDomain, seed)
	if err != nil {
		return "", "", "", err
	}
	publishID, err := ids.DeterministicUUID(key, publishDomain, seed)
	return eventID, outboxID, publishID, err
}
