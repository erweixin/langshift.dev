package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	userApprovalDecisionOperation  = "approvals.decide"
	adminApprovalDecisionOperation = "admin.approvals.decide"
)

type approvalDecisionSnapshot struct {
	status, kind, proposalHash, permissionSnapshot, requestedBy string
	version, targetVersion                                      uint64
	updatedAt, expiresAt, reauthenticatedAt                     time.Time
}

func (service ApprovalControlService) DecideApproval(ctx context.Context, command api.DecideApprovalCommand) (api.ApprovalResult, error) {
	if !service.validDecisionService() || !validApprovalServiceCommand(command) {
		return api.ApprovalResult{}, api.ErrValidation
	}
	operationID := userApprovalDecisionOperation
	if command.Mode == "admin" {
		operationID = adminApprovalDecisionOperation
	}
	canonical, _ := json.Marshal(struct {
		ClientRequestID, ApprovalID, Decision, ProposalHash, Mode, PermissionSnapshot string
		ExpectedApprovalVersion, TargetVersion                                        uint64
	}{command.ClientRequestID, command.ApprovalID, command.Decision, command.ProposalHash, command.Mode, command.PermissionSnapshot, command.ExpectedApprovalVersion, command.TargetVersion})
	adapter := service.idempotencyAdapter()
	input, descriptor, err := adapter.idempotencyInput(command.TenantID, command.UserID, operationID, command.IdempotencyKey, command.RequestID, canonical)
	if err != nil {
		return api.ApprovalResult{}, api.ErrDependencyUnavailable
	}
	if result, found, loadErr := adapter.loadRepairResponse(ctx, input, descriptor); loadErr != nil {
		return api.ApprovalResult{}, mapApprovalServiceError(loadErr)
	} else if found {
		return result, nil
	}

	snapshot, err := service.loadApprovalDecisionSnapshot(ctx, command)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.ApprovalResult{}, api.ErrResourceNotFound
	}
	if err != nil {
		return api.ApprovalResult{}, mapApprovalServiceError(err)
	}
	if snapshot.version != command.ExpectedApprovalVersion {
		return api.ApprovalResult{}, api.ErrVersionConflict
	}
	if snapshot.status != "pending" || !snapshot.expiresAt.After(service.now()) || snapshot.proposalHash != command.ProposalHash || snapshot.targetVersion != command.TargetVersion {
		return api.ApprovalResult{}, api.ErrStateConflict
	}
	if command.Mode == "user" {
		if command.UserID != snapshot.requestedBy {
			return api.ApprovalResult{}, api.ErrPermissionDenied
		}
		command.PermissionSnapshot = snapshot.permissionSnapshot
	} else if command.PermissionSnapshot != snapshot.permissionSnapshot {
		return api.ApprovalResult{}, api.ErrStateConflict
	}
	if snapshot.reauthenticatedAt.After(service.now()) || service.now().Sub(snapshot.reauthenticatedAt) > approvalReauthenticationMaxAge {
		return api.ApprovalResult{}, api.ErrReauthenticationRequired
	}
	decisionID, err := ids.DeterministicUUID(service.IDKey, "approval-decision", input.RecordID)
	if err != nil {
		return api.ApprovalResult{}, api.ErrDependencyUnavailable
	}
	eventIDs, err := service.Store.approvalEventIDs("decision", decisionID)
	if err != nil {
		return api.ApprovalResult{}, api.ErrDependencyUnavailable
	}
	payloadValue := approvalDecisionPayload(command, snapshot)
	eventPayload, err := service.putApprovalJSON(ctx, command.TenantID, eventIDs.event, payloadValue)
	if err != nil {
		return api.ApprovalResult{}, api.ErrDependencyUnavailable
	}
	completed, err := adapter.beginRepairIdempotency(ctx, input)
	if err != nil {
		return api.ApprovalResult{}, mapApprovalServiceError(err)
	}
	if completed {
		result, found, loadErr := adapter.loadRepairResponse(ctx, input, descriptor)
		if loadErr != nil || !found {
			return api.ApprovalResult{}, api.ErrDependencyUnavailable
		}
		return result, nil
	}
	actor, _ := json.Marshal(map[string]string{"kind": "user", "user_id": command.UserID, "session_id": command.SessionID, "authorization_mode": command.Mode})
	storeCommand := DecideApprovalCommand{
		ApprovalID: command.ApprovalID, TenantID: command.TenantID, DecisionID: decisionID,
		ActorUserID: command.UserID, SessionID: command.SessionID, Decision: command.Decision, Mode: command.Mode,
		ProposalHash: snapshot.proposalHash, PermissionSnapshot: snapshot.permissionSnapshot,
		ExpectedApprovalVersion: command.ExpectedApprovalVersion, TargetVersion: snapshot.targetVersion,
		ReauthenticatedAt: snapshot.reauthenticatedAt, Actor: actor, CorrelationID: command.RequestID, DecisionEvent: eventPayload,
	}
	var decided DecidedApproval
	for attempt := 0; attempt < 8; attempt++ {
		decided, err = service.Store.DecideApproval(ctx, storeCommand)
		if !isSerializationFailure(err) {
			break
		}
	}
	if err != nil {
		return api.ApprovalResult{}, mapApprovalServiceError(err)
	}
	result := api.ApprovalResult{ID: decided.ApprovalID, Version: decided.Version, Status: decided.Status, UpdatedAt: decided.UpdatedAt}
	return adapter.completeAndReadRepairResponse(ctx, input, descriptor, result)
}

func isSerializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "40001"
}

func (service ApprovalControlService) loadApprovalDecisionSnapshot(ctx context.Context, command api.DecideApprovalCommand) (approvalDecisionSnapshot, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return approvalDecisionSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return approvalDecisionSnapshot{}, err
	}
	var value approvalDecisionSnapshot
	err = tx.QueryRow(ctx, `SELECT status,version,approval_kind,proposal_hash,target_version,permission_snapshot,requested_by::text,updated_at,expires_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.ApprovalID).Scan(&value.status, &value.version, &value.kind, &value.proposalHash, &value.targetVersion, &value.permissionSnapshot, &value.requestedBy, &value.updatedAt, &value.expiresAt)
	if err != nil {
		return approvalDecisionSnapshot{}, err
	}
	err = tx.QueryRow(ctx, `SELECT reauthenticated_at FROM identity.sessions WHERE id=$1 AND user_id=$2 AND active_tenant_id=$3 AND revoked_at IS NULL AND expires_at>$4 AND reauthenticated_at IS NOT NULL`, command.SessionID, command.UserID, command.TenantID, service.now()).Scan(&value.reauthenticatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return approvalDecisionSnapshot{}, api.ErrReauthenticationRequired
	}
	if err != nil {
		return approvalDecisionSnapshot{}, err
	}
	return value, tx.Commit(ctx)
}

func approvalDecisionPayload(command api.DecideApprovalCommand, snapshot approvalDecisionSnapshot) map[string]any {
	value := map[string]any{"subject_id": command.ApprovalID, "subject_version": snapshot.version + 1, "proposal_hash": snapshot.proposalHash}
	switch command.Decision {
	case "approve":
		value["request_id"] = command.ClientRequestID
		value["approval_id"] = command.ApprovalID
		value["approver_user_id"] = command.UserID
		value["target_version"] = snapshot.targetVersion
		value["permission_snapshot"] = snapshot.permissionSnapshot
		value["reauthenticated_at"] = snapshot.reauthenticatedAt
	case "revise":
		value["previous_state"] = snapshot.status
		value["new_state"] = "invalidated"
		value["reason_code"] = "revision_requested"
	}
	return value
}

func (service ApprovalControlService) idempotencyAdapter() RepairControlService {
	return RepairControlService{Pool: service.Pool, Payloads: service.Payloads, IDKey: service.IDKey, IdempotencyKeyPepper: service.IdempotencyKeyPepper, RequestDigestPepper: service.RequestDigestPepper, IdempotencyTTL: service.IdempotencyTTL, Now: service.Now}
}

func (service ApprovalControlService) validDecisionService() bool {
	return service.Pool != nil && service.Pool.Stat().MaxConns() >= 2 && service.Payloads != nil && service.Store.valid() && len(service.IDKey) >= 32 && bytes.Equal(service.IDKey, service.Store.IDKey) && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0
}

func validApprovalServiceCommand(command api.DecideApprovalCommand) bool {
	decision := command.Decision == "approve" || command.Decision == "reject" || command.Decision == "revise"
	mode := command.Mode == "user" || command.Mode == "admin" && command.Decision != "revise"
	permission := command.Mode == "user" && command.PermissionSnapshot == "" || command.Mode == "admin" && command.PermissionSnapshot != ""
	return command.RequestID != "" && command.ClientRequestID != "" && command.IdempotencyKey != "" && command.TenantID != "" && command.UserID != "" && command.SessionID != "" && command.ApprovalID != "" && decision && mode && permission && command.ProposalHash != "" && command.ExpectedApprovalVersion > 0 && command.TargetVersion > 0
}

func mapApprovalServiceError(err error) error {
	switch {
	case errors.Is(err, idempotency.ErrKeyConflict):
		return api.ErrIdempotencyConflict
	case errors.Is(err, api.ErrReauthenticationRequired), errors.Is(err, ErrApprovalAuthorization):
		return api.ErrReauthenticationRequired
	case errors.Is(err, ErrApprovalNotActionable), errors.Is(err, ErrApprovalConflict):
		return api.ErrStateConflict
	case errors.Is(err, ErrInvalidCommand):
		return api.ErrValidation
	default:
		return api.ErrDependencyUnavailable
	}
}

var _ api.ApprovalService = ApprovalControlService{}
