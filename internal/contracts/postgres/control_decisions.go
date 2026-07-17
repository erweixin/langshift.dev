package postgres

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	"github.com/langshift/lites/internal/platform/ids"
)

func (service ControlService) DecideContract(ctx context.Context, command contractsapi.ContractDecisionCommand) (contractsapi.Resource, error) {
	if uuid.Validate(command.ProposalID) != nil || (command.Decision != "approve" && command.Decision != "reject") || !validProposalHash(command.ProposalHash) || command.ExpectedProposalVersion != 1 {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	return service.execute(ctx, command.CommandMetadata, "admin.contracts.decide.v2", command, func(ctx context.Context, tx pgx.Tx, recordID string) (contractsapi.Resource, error) {
		now := service.now()
		role, reauthenticatedAt, err := service.currentPrincipal(ctx, tx, command.CommandMetadata, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		var status, proposalHash, targetID string
		var version, targetVersion uint64
		err = tx.QueryRow(ctx, `SELECT status,version,proposal_hash,target_contract_id::text,target_version FROM contracts.contract_proposals WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.ProposalID).Scan(&status, &version, &proposalHash, &targetID, &targetVersion)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		if status != "proposed" || version != command.ExpectedProposalVersion || proposalHash != command.ProposalHash || targetVersion != command.TargetVersion {
			return contractsapi.Resource{}, contractsapi.ErrApprovalScopeChanged
		}
		decisionID, _ := ids.DeterministicUUID(service.IDKey, "contract-approval-decision", recordID)
		permission := service.permissionSnapshot(command.CommandMetadata, role, reauthenticatedAt)
		body := map[string]any{"decision_id": decisionID, "proposal_id": command.ProposalID, "decision": command.Decision, "proposal_hash": proposalHash, "target_version": targetVersion, "permission_snapshot": permission, "decided_at": now}
		eventType := "ContractApprovalGranted"
		if command.Decision == "reject" {
			eventType = "ContractApprovalRejected"
		}
		eventID, err := service.appendEvent(ctx, tx, command.CommandMetadata, recordID, eventType, "contract_approval_decision", decisionID, 1, body, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO contracts.contract_approval_decisions(id,tenant_id,proposal_id,approver_user_id,membership_id,session_id,decision,proposal_hash,target_version,permission_snapshot,reauthenticated_at,decided_at,event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$12)`, decisionID, command.TenantID, command.ProposalID, command.UserID, command.MembershipID, command.SessionID, command.Decision, proposalHash, targetVersion, permission, reauthenticatedAt, now, eventID)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		var approvals int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM contracts.contract_approval_decisions WHERE tenant_id=$1 AND proposal_id=$2 AND decision='approve'`, command.TenantID, command.ProposalID).Scan(&approvals); err != nil {
			return contractsapi.Resource{}, err
		}
		result := contractsapi.Resource{ID: command.ProposalID, Version: 1, Status: "proposed", ProposalHash: proposalHash, TargetID: targetID, TargetVersion: targetVersion, ApprovalCount: approvals, UpdatedAt: now}
		if command.Decision == "reject" {
			result.Version, result.Status = 2, "rejected"
			return result, nil
		}
		if approvals == 2 {
			auditID, _ := ids.DeterministicUUID(service.IDKey, "contract-audit", recordID)
			var contractVersion uint64
			var contractStatus string
			if err = tx.QueryRow(ctx, `SELECT contract_id,contract_version,contract_status FROM contracts.apply_approved_contract_proposal($1,$2,$3,$4,$5)`, command.TenantID, command.ProposalID, command.UserID, auditID, now).Scan(&targetID, &contractVersion, &contractStatus); err != nil {
				return contractsapi.Resource{}, err
			}
			execution := map[string]any{"proposal_id": command.ProposalID, "contract_id": targetID, "contract_version": contractVersion, "contract_status": contractStatus, "proposal_hash": proposalHash, "approval_count": 2, "executed_at": now}
			if _, err = service.appendEvent(ctx, tx, command.CommandMetadata, recordID, "ContractProposalExecuted", "contract_proposal", command.ProposalID, 2, execution, now); err != nil {
				return contractsapi.Resource{}, err
			}
			result.Version, result.Status, result.TargetVersion = 2, "executed", contractVersion
		}
		return result, nil
	})
}

func (service ControlService) DecideAdjustment(ctx context.Context, command contractsapi.AdjustmentDecisionCommand) (contractsapi.Resource, error) {
	if uuid.Validate(command.ProposalID) != nil || (command.Decision != "approve" && command.Decision != "reject") || !validProposalHash(command.ProposalHash) || command.TargetVersion < 1 || command.ExpectedProposalVersion != 1 {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	return service.execute(ctx, command.CommandMetadata, "admin.usage.adjustments.decide.v2", command, func(ctx context.Context, tx pgx.Tx, recordID string) (contractsapi.Resource, error) {
		now := service.now()
		role, reauthenticatedAt, err := service.currentPrincipal(ctx, tx, command.CommandMetadata, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		var status, proposalHash, bucketID string
		var version, targetVersion uint64
		err = tx.QueryRow(ctx, `SELECT status,version,proposal_hash,bucket_id::text,target_bucket_version FROM contracts.accounting_adjustment_proposals WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.ProposalID).Scan(&status, &version, &proposalHash, &bucketID, &targetVersion)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		if status != "proposed" || version != command.ExpectedProposalVersion || proposalHash != command.ProposalHash || targetVersion != command.TargetVersion {
			return contractsapi.Resource{}, contractsapi.ErrApprovalScopeChanged
		}
		decisionID, _ := ids.DeterministicUUID(service.IDKey, "accounting-adjustment-decision", recordID)
		permission := service.permissionSnapshot(command.CommandMetadata, role, reauthenticatedAt)
		body := map[string]any{"decision_id": decisionID, "proposal_id": command.ProposalID, "decision": command.Decision, "proposal_hash": proposalHash, "target_bucket_version": targetVersion, "permission_snapshot": permission, "decided_at": now}
		eventType := "AccountingAdjustmentApprovalGranted"
		if command.Decision == "reject" {
			eventType = "AccountingAdjustmentApprovalRejected"
		}
		eventID, err := service.appendEvent(ctx, tx, command.CommandMetadata, recordID, eventType, "accounting_adjustment_approval_decision", decisionID, 1, body, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO contracts.accounting_adjustment_approval_decisions(id,tenant_id,proposal_id,approver_user_id,membership_id,session_id,decision,proposal_hash,target_bucket_version,permission_snapshot,reauthenticated_at,decided_at,event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$12)`, decisionID, command.TenantID, command.ProposalID, command.UserID, command.MembershipID, command.SessionID, command.Decision, proposalHash, targetVersion, permission, reauthenticatedAt, now, eventID)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		var approvals int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM contracts.accounting_adjustment_approval_decisions WHERE tenant_id=$1 AND proposal_id=$2 AND decision='approve'`, command.TenantID, command.ProposalID).Scan(&approvals); err != nil {
			return contractsapi.Resource{}, err
		}
		result := contractsapi.Resource{ID: command.ProposalID, Version: 1, Status: "proposed", ProposalHash: proposalHash, TargetID: bucketID, TargetVersion: targetVersion, ApprovalCount: approvals, UpdatedAt: now}
		if command.Decision == "reject" {
			result.Version, result.Status = 2, "rejected"
			return result, nil
		}
		if approvals == 2 {
			adjustmentID, _ := ids.DeterministicUUID(service.IDKey, "manual-adjustment", recordID)
			var bucketVersion uint64
			var granted int64
			if err = tx.QueryRow(ctx, `SELECT bucket_id,bucket_version,granted_units FROM contracts.apply_approved_accounting_adjustment($1,$2,$3,$4,$5)`, command.TenantID, command.ProposalID, command.UserID, adjustmentID, now).Scan(&bucketID, &bucketVersion, &granted); err != nil {
				return contractsapi.Resource{}, err
			}
			execution := map[string]any{"proposal_id": command.ProposalID, "bucket_id": bucketID, "bucket_version": bucketVersion, "granted_units": granted, "proposal_hash": proposalHash, "approval_count": 2, "executed_at": now}
			if _, err = service.appendEvent(ctx, tx, command.CommandMetadata, recordID, "AccountingAdjustmentExecuted", "accounting_adjustment_proposal", command.ProposalID, 2, execution, now); err != nil {
				return contractsapi.Resource{}, err
			}
			result.Version, result.Status, result.TargetVersion = 2, "executed", bucketVersion
		}
		return result, nil
	})
}

func validProposalHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}
