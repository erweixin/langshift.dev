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
	direct                                                      bool
	existingDecision                                            bool
	decisionEventID                                             string
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
	decisionID, err := ids.DeterministicUUID(service.IDKey, "approval-decision", input.RecordID)
	if err != nil {
		return api.ApprovalResult{}, api.ErrDependencyUnavailable
	}

	snapshot, err := service.loadApprovalDecisionSnapshot(ctx, command, decisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.ApprovalResult{}, api.ErrResourceNotFound
	}
	if err != nil {
		return api.ApprovalResult{}, mapApprovalServiceError(err)
	}
	if (!snapshot.existingDecision && snapshot.version != command.ExpectedApprovalVersion) || (snapshot.existingDecision && snapshot.version != command.ExpectedApprovalVersion+1) {
		return api.ApprovalResult{}, api.ErrVersionConflict
	}
	expectedStatus, _, _ := approvalDecisionOutcome(command.Decision)
	if (!snapshot.existingDecision && (snapshot.status != "pending" || !snapshot.expiresAt.After(service.now()))) || (snapshot.existingDecision && snapshot.status != expectedStatus) || snapshot.proposalHash != command.ProposalHash || snapshot.targetVersion != command.TargetVersion {
		return api.ApprovalResult{}, api.ErrStateConflict
	}
	if snapshot.direct && command.Decision == "revise" {
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
	if !snapshot.existingDecision && (snapshot.reauthenticatedAt.After(service.now()) || service.now().Sub(snapshot.reauthenticatedAt) > approvalReauthenticationMaxAge) {
		return api.ApprovalResult{}, api.ErrReauthenticationRequired
	}
	eventIDs, err := service.Store.approvalEventIDs("decision", decisionID)
	if err != nil {
		return api.ApprovalResult{}, api.ErrDependencyUnavailable
	}
	if snapshot.existingDecision && snapshot.decisionEventID != eventIDs.event {
		return api.ApprovalResult{}, api.ErrStateConflict
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
	if snapshot.direct {
		if err = service.applyDirectApprovalDecision(ctx, command, snapshot, decided, actor); err != nil {
			return api.ApprovalResult{}, mapApprovalServiceError(err)
		}
	}
	result := api.ApprovalResult{ID: decided.ApprovalID, Version: decided.Version, Status: decided.Status, UpdatedAt: decided.UpdatedAt}
	return adapter.completeAndReadRepairResponse(ctx, input, descriptor, result)
}

func isSerializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "40001"
}

func (service ApprovalControlService) loadApprovalDecisionSnapshot(ctx context.Context, command api.DecideApprovalCommand, decisionID string) (approvalDecisionSnapshot, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return approvalDecisionSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return approvalDecisionSnapshot{}, err
	}
	var value approvalDecisionSnapshot
	err = tx.QueryRow(ctx, `SELECT a.status,a.version,a.approval_kind,a.proposal_hash,a.target_version,a.permission_snapshot,a.requested_by::text,a.updated_at,a.expires_at,EXISTS(SELECT 1 FROM agent.tool_proposals p WHERE p.tenant_id=a.tenant_id AND p.approval_id=a.id) FROM agent.approvals a WHERE a.tenant_id=$1 AND a.id=$2`, command.TenantID, command.ApprovalID).Scan(&value.status, &value.version, &value.kind, &value.proposalHash, &value.targetVersion, &value.permissionSnapshot, &value.requestedBy, &value.updatedAt, &value.expiresAt, &value.direct)
	if err != nil {
		return approvalDecisionSnapshot{}, err
	}
	if value.status != "pending" {
		var decision, mode, proposalHash, permissionSnapshot, sessionID string
		var targetVersion uint64
		err = tx.QueryRow(ctx, `SELECT decision,decision_mode,proposal_hash,target_version,permission_snapshot,session_id::text,reauthenticated_at,decision_event_id::text FROM agent.approval_decisions WHERE tenant_id=$1 AND id=$2 AND approval_id=$3 AND actor_user_id=$4`, command.TenantID, decisionID, command.ApprovalID, command.UserID).Scan(&decision, &mode, &proposalHash, &targetVersion, &permissionSnapshot, &sessionID, &value.reauthenticatedAt, &value.decisionEventID)
		if err != nil {
			return approvalDecisionSnapshot{}, err
		}
		if decision != command.Decision || mode != command.Mode || proposalHash != value.proposalHash || targetVersion != value.targetVersion || permissionSnapshot != value.permissionSnapshot || sessionID != command.SessionID {
			return approvalDecisionSnapshot{}, ErrApprovalConflict
		}
		value.existingDecision = true
		return value, tx.Commit(ctx)
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

func (service ApprovalControlService) applyDirectApprovalDecision(ctx context.Context, command api.DecideApprovalCommand, snapshot approvalDecisionSnapshot, decided DecidedApproval, actor json.RawMessage) error {
	switch command.Decision {
	case "approve":
		identifier, err := service.Store.directAuthorizationIdentifiers(command.ApprovalID)
		if err != nil {
			return err
		}
		toolEvent, err := service.putApprovalJSON(ctx, command.TenantID, identifier.toolEvent, map[string]any{"approval_id": command.ApprovalID, "proposal_hash": snapshot.proposalHash, "target_version": snapshot.targetVersion + 1, "authorization_event_id": decided.EventID})
		if err != nil {
			return err
		}
		groupEvent, err := service.putApprovalJSON(ctx, command.TenantID, identifier.groupEvent, map[string]any{"approval_id": command.ApprovalID, "proposal_hash": snapshot.proposalHash, "state": "execution"})
		if err != nil {
			return err
		}
		runEvent, err := service.putApprovalJSON(ctx, command.TenantID, identifier.runEvent, map[string]any{"approval_id": command.ApprovalID, "proposal_hash": snapshot.proposalHash, "state": "waiting_tool"})
		if err != nil {
			return err
		}
		storeCommand := AuthorizeDirectToolCommand{
			TenantID: command.TenantID, ApprovalID: command.ApprovalID, ApprovalEventID: decided.EventID,
			ProposalHash: snapshot.proposalHash, PermissionSnapshot: snapshot.permissionSnapshot,
			ExpectedApprovalVersion: decided.Version, ExpectedToolVersion: snapshot.targetVersion,
			Actor: actor, CorrelationID: command.RequestID, ToolRequestedEvent: toolEvent,
			GroupAuthorizedEvent: groupEvent, RunWaitingToolEvent: runEvent,
		}
		for attempt := 0; attempt < 8; attempt++ {
			_, err = service.Store.AuthorizeDirectTool(ctx, storeCommand)
			if !isSerializationFailure(err) {
				return err
			}
		}
		return err
	case "reject":
		identifier, err := service.Store.directRejectionIdentifiers(command.ApprovalID)
		if err != nil {
			return err
		}
		toolEvent, err := service.putApprovalJSON(ctx, command.TenantID, identifier.runEvent+":tools", map[string]any{"approval_id": command.ApprovalID, "proposal_hash": snapshot.proposalHash, "reason_code": "approval_rejected", "state": "cancelled"})
		if err != nil {
			return err
		}
		runEvent, err := service.putApprovalJSON(ctx, command.TenantID, identifier.runEvent, map[string]any{"approval_id": command.ApprovalID, "proposal_hash": snapshot.proposalHash, "reason_code": "approval_rejected", "state": "cancelled"})
		if err != nil {
			return err
		}
		storeCommand := RejectDirectApprovalGroupCommand{TenantID: command.TenantID, ApprovalID: command.ApprovalID, ApprovalEventID: decided.EventID, ExpectedApprovalVersion: decided.Version, Actor: actor, CorrelationID: command.RequestID, ToolCancelledEvent: toolEvent, RunCancelledEvent: runEvent}
		for attempt := 0; attempt < 8; attempt++ {
			_, err = service.Store.RejectDirectApprovalGroup(ctx, storeCommand)
			if !isSerializationFailure(err) {
				return err
			}
		}
		return err
	default:
		return ErrInvalidCommand
	}
}

func approvalDecisionPayload(command api.DecideApprovalCommand, snapshot approvalDecisionSnapshot) map[string]any {
	value := map[string]any{"subject_id": command.ApprovalID, "subject_version": command.ExpectedApprovalVersion + 1, "proposal_hash": snapshot.proposalHash}
	switch command.Decision {
	case "approve":
		value["request_id"] = command.ClientRequestID
		value["approval_id"] = command.ApprovalID
		value["approver_user_id"] = command.UserID
		value["target_version"] = snapshot.targetVersion
		value["permission_snapshot"] = snapshot.permissionSnapshot
		value["reauthenticated_at"] = snapshot.reauthenticatedAt
	case "revise":
		value["previous_state"] = "pending"
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
	case errors.Is(err, ErrDirectApprovalAuthorization), errors.Is(err, ErrDirectApprovalConflict):
		return api.ErrStateConflict
	case errors.Is(err, ErrInvalidCommand):
		return api.ErrValidation
	default:
		return api.ErrDependencyUnavailable
	}
}

var _ api.ApprovalService = ApprovalControlService{}
