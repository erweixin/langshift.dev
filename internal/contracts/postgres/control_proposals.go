package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

func (service ControlService) ProposeContract(ctx context.Context, command contractsapi.ContractProposalCommand) (contractsapi.Resource, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	command.ContractNumber = strings.TrimSpace(command.ContractNumber)
	command.Region = strings.TrimSpace(command.Region)
	if !validContractCommand(command) {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	return service.execute(ctx, command.CommandMetadata, "admin.contracts.propose.v2", command, func(ctx context.Context, tx pgx.Tx, recordID string) (contractsapi.Resource, error) {
		now := service.now()
		_, reauthenticatedAt, err := service.currentPrincipal(ctx, tx, command.CommandMetadata, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		proposalID, _ := ids.DeterministicUUID(service.IDKey, "contract-proposal", recordID)
		targetID := command.TargetContractID
		if command.Action == "create" {
			targetID, _ = ids.DeterministicUUID(service.IDKey, "contract-target", proposalID)
		}
		reasonHash := plaintextHash(command.Reason)
		reasonManifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: proposalID, Class: "contract-reason", ContentType: "text/plain; charset=utf-8"}, []byte(command.Reason))
		if err != nil {
			return contractsapi.Resource{}, err
		}
		expiresAt := now.Add(service.ProposalTTL)
		proposalHash := canonicalHash(map[string]any{"kind": "contract", "action": command.Action, "target_contract_id": targetID, "target_version": command.TargetVersion, "contract_number": command.ContractNumber, "starts_at": canonicalTime(command.StartsAt), "ends_at": canonicalTime(command.EndsAt), "seat_limit": command.SeatLimit, "region": command.Region, "license_kind": command.LicenseKind, "reason_hash": reasonHash, "expires_at": expiresAt.Format(time.RFC3339Nano)})
		contractNumber, startsAt, endsAt, seatLimit, region, licenseKind := contractTerms(command)
		_, err = tx.Exec(ctx, `INSERT INTO contracts.contract_proposals(id,tenant_id,created_at,updated_at,initiator_user_id,initiator_membership_id,initiator_session_id,reauthenticated_at,action,target_contract_id,target_version,status,proposal_hash,reason_ref,reason_hash,contract_number,starts_at,ends_at,seat_limit,region,license_kind,expires_at) VALUES($1,$2,$3,$3,$4,$5,$6,$7,$8,$9,$10,'proposed',$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, proposalID, command.TenantID, now, command.UserID, command.MembershipID, command.SessionID, reauthenticatedAt, command.Action, targetID, command.TargetVersion, proposalHash, reasonManifest.Ref, reasonHash, contractNumber, startsAt, endsAt, seatLimit, region, licenseKind, expiresAt)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		body := map[string]any{"proposal_id": proposalID, "action": command.Action, "target_contract_id": targetID, "target_version": command.TargetVersion, "proposal_hash": proposalHash, "reason_ref": reasonManifest.Ref, "reason_hash": reasonHash, "expires_at": expiresAt}
		if _, err = service.appendEvent(ctx, tx, command.CommandMetadata, recordID, "ContractProposalCreated", "contract_proposal", proposalID, 1, body, now); err != nil {
			return contractsapi.Resource{}, err
		}
		return contractsapi.Resource{ID: proposalID, Version: 1, Status: "proposed", ProposalHash: proposalHash, TargetID: targetID, TargetVersion: command.TargetVersion, UpdatedAt: now}, nil
	})
}

func (service ControlService) ProposeAdjustment(ctx context.Context, command contractsapi.AdjustmentProposalCommand) (contractsapi.Resource, error) {
	command.Reason = strings.TrimSpace(command.Reason)
	if uuid.Validate(command.BucketID) != nil || command.TargetVersion < 1 || command.Units == 0 || len(command.Reason) < 1 || len(command.Reason) > 1000 {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	return service.execute(ctx, command.CommandMetadata, "admin.usage.adjustments.propose.v2", command, func(ctx context.Context, tx pgx.Tx, recordID string) (contractsapi.Resource, error) {
		now := service.now()
		_, reauthenticatedAt, err := service.currentPrincipal(ctx, tx, command.CommandMetadata, now)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		proposalID, _ := ids.DeterministicUUID(service.IDKey, "accounting-adjustment-proposal", recordID)
		reasonHash := plaintextHash(command.Reason)
		reasonManifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: proposalID, Class: "accounting-adjustment-reason", ContentType: "text/plain; charset=utf-8"}, []byte(command.Reason))
		if err != nil {
			return contractsapi.Resource{}, err
		}
		expiresAt := now.Add(service.ProposalTTL)
		proposalHash := canonicalHash(map[string]any{"kind": "accounting_adjustment", "bucket_id": command.BucketID, "target_bucket_version": command.TargetVersion, "units": command.Units, "reason_hash": reasonHash, "expires_at": expiresAt.Format(time.RFC3339Nano)})
		_, err = tx.Exec(ctx, `INSERT INTO contracts.accounting_adjustment_proposals(id,tenant_id,created_at,updated_at,initiator_user_id,initiator_membership_id,initiator_session_id,reauthenticated_at,bucket_id,target_bucket_version,units,status,proposal_hash,reason_ref,reason_hash,expires_at) VALUES($1,$2,$3,$3,$4,$5,$6,$7,$8,$9,$10,'proposed',$11,$12,$13,$14)`, proposalID, command.TenantID, now, command.UserID, command.MembershipID, command.SessionID, reauthenticatedAt, command.BucketID, command.TargetVersion, command.Units, proposalHash, reasonManifest.Ref, reasonHash, expiresAt)
		if err != nil {
			return contractsapi.Resource{}, err
		}
		body := map[string]any{"proposal_id": proposalID, "bucket_id": command.BucketID, "target_bucket_version": command.TargetVersion, "units": command.Units, "proposal_hash": proposalHash, "reason_ref": reasonManifest.Ref, "reason_hash": reasonHash, "expires_at": expiresAt}
		if _, err = service.appendEvent(ctx, tx, command.CommandMetadata, recordID, "AccountingAdjustmentProposed", "accounting_adjustment_proposal", proposalID, 1, body, now); err != nil {
			return contractsapi.Resource{}, err
		}
		return contractsapi.Resource{ID: proposalID, Version: 1, Status: "proposed", ProposalHash: proposalHash, TargetID: command.BucketID, TargetVersion: command.TargetVersion, UpdatedAt: now}, nil
	})
}

func validContractCommand(command contractsapi.ContractProposalCommand) bool {
	if command.Action != "create" && command.Action != "renew" && command.Action != "suspend" && command.Action != "terminate" || len(command.Reason) < 1 || len(command.Reason) > 1000 {
		return false
	}
	if command.Action == "create" && (command.TargetContractID != "" || command.TargetVersion != 0) {
		return false
	}
	if command.Action != "create" && (uuid.Validate(command.TargetContractID) != nil || command.TargetVersion < 1) {
		return false
	}
	if command.Action == "suspend" || command.Action == "terminate" {
		return command.ContractNumber == "" && command.StartsAt == nil && command.EndsAt == nil && command.SeatLimit == 0 && command.Region == "" && command.LicenseKind == ""
	}
	return len(command.ContractNumber) >= 1 && len(command.ContractNumber) <= 200 && command.StartsAt != nil && command.EndsAt != nil && command.EndsAt.After(*command.StartsAt) && command.SeatLimit > 0 && len(command.Region) >= 1 && len(command.Region) <= 100 && (command.LicenseKind == "enterprise_cloud" || command.LicenseKind == "private_cloud" || command.LicenseKind == "commercial_self_hosted")
}

func contractTerms(command contractsapi.ContractProposalCommand) (any, any, any, any, any, any) {
	if command.Action == "suspend" || command.Action == "terminate" {
		return nil, nil, nil, nil, nil, nil
	}
	return command.ContractNumber, command.StartsAt.UTC(), command.EndsAt.UTC(), command.SeatLimit, command.Region, command.LicenseKind
}

func canonicalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
