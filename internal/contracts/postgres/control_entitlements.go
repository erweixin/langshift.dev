package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

func (service ControlService) ProposeEntitlement(ctx context.Context, command contractsapi.EntitlementProposalCommand) (contractsapi.Resource, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	var config map[string]any
	if uuid.Validate(command.ContractID) != nil || command.TargetContractVersion < 1 || !validEntitlementKey(command.EntitlementKey) || command.LimitValue != nil && *command.LimitValue < 0 || len(command.Config) < 2 || len(command.Config) > 16<<10 || json.Unmarshal(command.Config, &config) != nil || config == nil || len(command.Reason) < 1 || len(command.Reason) > 1000 {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	canonicalConfig, err := json.Marshal(config)
	if err != nil || len(canonicalConfig) > 16<<10 {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	command.Config = canonicalConfig
	return service.execute(ctx, command.CommandMetadata, "admin.entitlements.propose.v2", command, func(ctx context.Context, tx pgx.Tx, recordID string) (contractsapi.Resource, error) {
		now := service.now()
		_, reauthenticatedAt, err := service.currentPrincipal(ctx, tx, command.CommandMetadata, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		proposalID, _ := ids.DeterministicUUID(service.IDKey, "entitlement-proposal", recordID)
		reasonHash := plaintextHash(command.Reason)
		reasonManifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: proposalID, Class: "entitlement-reason", ContentType: "text/plain; charset=utf-8"}, []byte(command.Reason))
		if err != nil {
			return contractsapi.Resource{}, err
		}
		expiresAt := now.Add(service.ProposalTTL)
		proposalHash := canonicalHash(map[string]any{"kind": "entitlement", "contract_id": command.ContractID, "target_contract_version": command.TargetContractVersion, "entitlement_key": command.EntitlementKey, "target_entitlement_version": command.TargetEntitlementVersion, "limit_value": command.LimitValue, "config": json.RawMessage(canonicalConfig), "reason_hash": reasonHash, "expires_at": expiresAt.Format(time.RFC3339Nano)})
		_, err = tx.Exec(ctx, `INSERT INTO contracts.entitlement_proposals(id,tenant_id,created_at,updated_at,initiator_user_id,initiator_membership_id,initiator_session_id,reauthenticated_at,contract_id,target_contract_version,entitlement_key,target_entitlement_version,limit_value,config,status,proposal_hash,reason_ref,reason_hash,expires_at) VALUES($1,$2,$3,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'proposed',$14,$15,$16,$17)`, proposalID, command.TenantID, now, command.UserID, command.MembershipID, command.SessionID, reauthenticatedAt, command.ContractID, command.TargetContractVersion, command.EntitlementKey, command.TargetEntitlementVersion, command.LimitValue, canonicalConfig, proposalHash, reasonManifest.Ref, reasonHash, expiresAt)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		body := map[string]any{"proposal_id": proposalID, "contract_id": command.ContractID, "target_contract_version": command.TargetContractVersion, "entitlement_key": command.EntitlementKey, "target_entitlement_version": command.TargetEntitlementVersion, "limit_value": command.LimitValue, "config_hash": plaintextHash(string(canonicalConfig)), "proposal_hash": proposalHash, "reason_ref": reasonManifest.Ref, "reason_hash": reasonHash, "expires_at": expiresAt}
		if _, err = service.appendEvent(ctx, tx, command.CommandMetadata, recordID, "EntitlementProposed", "entitlement_proposal", proposalID, 1, body, now); err != nil {
			return contractsapi.Resource{}, err
		}
		return contractsapi.Resource{ID: proposalID, Version: 1, Status: "proposed", ProposalHash: proposalHash, TargetID: command.ContractID, TargetVersion: command.TargetContractVersion, UpdatedAt: now}, nil
	})
}

func (service ControlService) DecideEntitlement(ctx context.Context, command contractsapi.EntitlementDecisionCommand) (contractsapi.Resource, error) {
	if uuid.Validate(command.ProposalID) != nil || (command.Decision != "approve" && command.Decision != "reject") || !validProposalHash(command.ProposalHash) || command.TargetContractVersion < 1 || command.ExpectedProposalVersion != 1 {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	return service.execute(ctx, command.CommandMetadata, "admin.entitlements.decide.v2", command, func(ctx context.Context, tx pgx.Tx, recordID string) (contractsapi.Resource, error) {
		now := service.now()
		role, reauthenticatedAt, err := service.currentPrincipal(ctx, tx, command.CommandMetadata, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		var status, proposalHash, contractID string
		var version, contractVersion, entitlementVersion uint64
		err = tx.QueryRow(ctx, `SELECT status,version,proposal_hash,contract_id::text,target_contract_version,target_entitlement_version FROM contracts.entitlement_proposals WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.ProposalID).Scan(&status, &version, &proposalHash, &contractID, &contractVersion, &entitlementVersion)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		if status != "proposed" || version != command.ExpectedProposalVersion || proposalHash != command.ProposalHash || contractVersion != command.TargetContractVersion || entitlementVersion != command.TargetEntitlementVersion {
			return contractsapi.Resource{}, contractsapi.ErrApprovalScopeChanged
		}
		decisionID, _ := ids.DeterministicUUID(service.IDKey, "entitlement-approval-decision", recordID)
		permission := service.permissionSnapshot(command.CommandMetadata, role, reauthenticatedAt)
		body := map[string]any{"decision_id": decisionID, "proposal_id": command.ProposalID, "decision": command.Decision, "proposal_hash": proposalHash, "target_contract_version": contractVersion, "target_entitlement_version": entitlementVersion, "permission_snapshot": permission, "decided_at": now}
		eventType := "EntitlementApprovalGranted"
		if command.Decision == "reject" {
			eventType = "EntitlementApprovalRejected"
		}
		eventID, err := service.appendEvent(ctx, tx, command.CommandMetadata, recordID, eventType, "entitlement_approval_decision", decisionID, 1, body, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO contracts.entitlement_approval_decisions(id,tenant_id,proposal_id,approver_user_id,membership_id,session_id,decision,proposal_hash,target_contract_version,target_entitlement_version,permission_snapshot,reauthenticated_at,decided_at,event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$13)`, decisionID, command.TenantID, command.ProposalID, command.UserID, command.MembershipID, command.SessionID, command.Decision, proposalHash, contractVersion, entitlementVersion, permission, reauthenticatedAt, now, eventID)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		var approvals int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM contracts.entitlement_approval_decisions WHERE tenant_id=$1 AND proposal_id=$2 AND decision='approve'`, command.TenantID, command.ProposalID).Scan(&approvals); err != nil {
			return contractsapi.Resource{}, err
		}
		result := contractsapi.Resource{ID: command.ProposalID, Version: 1, Status: "proposed", ProposalHash: proposalHash, TargetID: contractID, TargetVersion: contractVersion, ApprovalCount: approvals, UpdatedAt: now}
		if command.Decision == "reject" {
			result.Version, result.Status = 2, "rejected"
			return result, nil
		}
		if approvals == 2 {
			entitlementID, _ := ids.DeterministicUUID(service.IDKey, "contract-entitlement", command.TenantID+"\x00"+contractID+"\x00"+command.ProposalID)
			auditID, _ := ids.DeterministicUUID(service.IDKey, "entitlement-audit", recordID)
			var appliedID string
			var appliedVersion uint64
			if err = tx.QueryRow(ctx, `SELECT entitlement_id,entitlement_version FROM contracts.apply_approved_entitlement_proposal($1,$2,$3,$4,$5,$6)`, command.TenantID, command.ProposalID, command.UserID, entitlementID, auditID, now).Scan(&appliedID, &appliedVersion); err != nil {
				return contractsapi.Resource{}, err
			}
			execution := map[string]any{"proposal_id": command.ProposalID, "contract_id": contractID, "contract_version": contractVersion, "entitlement_id": appliedID, "entitlement_version": appliedVersion, "proposal_hash": proposalHash, "approval_count": 2, "executed_at": now}
			if _, err = service.appendEvent(ctx, tx, command.CommandMetadata, recordID, "EntitlementProposalExecuted", "entitlement_proposal", command.ProposalID, 2, execution, now); err != nil {
				return contractsapi.Resource{}, err
			}
			result.Version, result.Status, result.TargetID, result.TargetVersion = 2, "executed", appliedID, appliedVersion
		}
		return result, nil
	})
}

func validEntitlementKey(value string) bool {
	switch value {
	case "programs", "cohorts", "role_packs", "aggregate_analytics", "audit_export", "private_delivery", "commercial_license", "support_tier":
		return true
	default:
		return false
	}
}
