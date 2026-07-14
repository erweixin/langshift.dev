package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	repairProposalOperation = "repair.propose"
	repairDecisionOperation = "repair.decide"
	repairResponseClass     = "execution-idempotency"
	repairEventClass        = "event-payload"
	repairEvidenceClass     = "repair-evidence"
)

// RepairControlService is the authenticated application boundary for the
// immutable, dual-control tool-effect repair workflow.
type RepairControlService struct {
	Pool                 *pgxpool.Pool
	Store                RunStore
	Payloads             payload.Store
	IDKey                []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	IdempotencyTTL       time.Duration
	ProposalTTL          time.Duration
	Now                  func() time.Time

	ResumeQueueClass    string
	ResumeResourceClass string
	ResumePriority      int
	ResumeCostUnits     int64
	ResumeMaxAttempts   int
}

type repairTarget struct {
	ToolCallID, UserID, RunID, EffectID, EffectKey string
	ToolVersion, EffectVersion                     uint64
}

type repairSnapshot struct {
	ID, Status, Resolution, ProposalHash, EvidenceHash string
	TargetID, EffectKey, ResidualRiskRef, InitiatorID  string
	Version, TargetVersion, EffectVersion              uint64
	UpdatedAt, ExpiresAt                               time.Time
}

type repairIdempotencyInput struct {
	RecordID, TenantID, UserID, OperationID string
	RawKey, RequestHash, RequestID          string
}

func (service RepairControlService) ProposeRepair(ctx context.Context, command api.ProposeRepairCommand) (api.RepairResult, error) {
	if err := service.validatePropose(command); err != nil {
		return api.RepairResult{}, err
	}
	canonical, _ := json.Marshal(struct {
		ClientRequestID, TargetID, Resolution, EvidenceHash, Reason string
		TargetVersion                                               uint64
	}{command.ClientRequestID, command.TargetID, command.Resolution, command.EvidenceHash, command.Reason, command.TargetVersion})
	input, descriptor, err := service.idempotencyInput(command.TenantID, command.UserID, repairProposalOperation, command.IdempotencyKey, command.RequestID, canonical)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadRepairResponse(ctx, input, descriptor); loadErr != nil {
		return api.RepairResult{}, mapRepairServiceError(loadErr)
	} else if found {
		return result, nil
	}

	target, err := service.loadRepairTarget(ctx, command.TenantID, command.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.RepairResult{}, api.ErrResourceNotFound
	}
	if errors.Is(err, ErrRepairNotActionable) {
		return api.RepairResult{}, api.ErrStateConflict
	}
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	if target.ToolVersion != command.TargetVersion {
		return api.RepairResult{}, api.ErrVersionConflict
	}
	repairID, err := ids.DeterministicUUID(service.IDKey, "repair-command", input.RecordID)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	reasonHash, err := idempotency.RequestDigest([]byte(command.Reason), service.RequestDigestPepper)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	proposalCanonical, _ := json.Marshal(struct {
		RepairID, ToolCallID, EffectID, EffectKey, Resolution, EvidenceHash, ReasonHash string
		ToolVersion, EffectVersion                                                      uint64
	}{repairID, target.ToolCallID, target.EffectID, target.EffectKey, command.Resolution, command.EvidenceHash, reasonHash, target.ToolVersion, target.EffectVersion})
	proposalHash, err := idempotency.RequestDigest(proposalCanonical, service.RequestDigestPepper)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	reason, err := service.putJSON(ctx, command.TenantID, repairID, repairEvidenceClass, map[string]any{"repair_command_id": repairID, "evidence_hash": command.EvidenceHash, "reason": command.Reason, "resolution": command.Resolution})
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	proposedEventID, _, _, err := repairProposalEventIDs(service.Store.IDKey, repairID)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	proposedPayload, err := service.putJSON(ctx, command.TenantID, proposedEventID, repairEventClass, map[string]any{"subject_id": repairID, "subject_version": 1, "request_id": command.ClientRequestID, "command_id": nil, "proposal_hash": proposalHash})
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	completed, err := service.beginRepairIdempotency(ctx, input)
	if err != nil {
		return api.RepairResult{}, mapRepairServiceError(err)
	}
	if completed {
		result, found, loadErr := service.loadRepairResponse(ctx, input, descriptor)
		if loadErr != nil || !found {
			return api.RepairResult{}, api.ErrDependencyUnavailable
		}
		return result, nil
	}
	now := service.now()
	residualRiskRef := ""
	if command.Resolution == "accepted_unknown" {
		residualRiskRef = reason.Ref
	}
	proposed, err := service.Store.ProposeToolEffectRepair(ctx, ProposeToolEffectRepairCommand{
		RepairID: repairID, TenantID: command.TenantID, InitiatorUserID: command.UserID, InitiatorSessionID: command.SessionID,
		ToolCallID: target.ToolCallID, ExpectedToolVersion: target.ToolVersion, ExpectedEffectVersion: target.EffectVersion,
		EffectKey: target.EffectKey, Resolution: command.Resolution, ProposalHash: proposalHash, EvidenceHash: command.EvidenceHash,
		ResidualRiskRef: residualRiskRef, ExpiresAt: now.Add(service.proposalTTL()), Actor: repairActor(command.UserID, command.SessionID),
		CorrelationID: command.RequestID, ProposedEvent: proposedPayload,
	})
	if errors.Is(err, ErrRepairConflict) {
		// A concurrent exact request may have selected a different encrypted
		// evidence envelope or timestamp. The deterministic repair id and the
		// idempotency request hash make an exact durable scope safe to adopt.
		var current repairSnapshot
		current, err = service.loadRepairSnapshot(ctx, command.TenantID, repairID)
		if err == nil && current.ProposalHash == proposalHash && current.TargetID == target.ToolCallID && current.TargetVersion == target.ToolVersion && current.EffectVersion == target.EffectVersion && current.EffectKey == target.EffectKey && current.Resolution == command.Resolution && current.EvidenceHash == command.EvidenceHash && current.InitiatorID == command.UserID {
			proposed = ProposedRepair{RepairID: current.ID, Status: current.Status, Resolution: current.Resolution, ProposalHash: current.ProposalHash, Version: current.Version, Replayed: true}
			err = nil
		}
	}
	if err != nil {
		return api.RepairResult{}, mapRepairServiceError(err)
	}
	current, err := service.loadRepairSnapshot(ctx, command.TenantID, proposed.RepairID)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	result := api.RepairResult{ID: current.ID, Version: current.Version, Status: current.Status, UpdatedAt: current.UpdatedAt}
	return service.completeAndReadRepairResponse(ctx, input, descriptor, result)
}

func (service RepairControlService) DecideRepair(ctx context.Context, command api.DecideRepairCommand) (api.RepairResult, error) {
	if err := service.validateDecision(command); err != nil {
		return api.RepairResult{}, err
	}
	canonical, _ := json.Marshal(struct {
		ClientRequestID, RepairID, Decision, ProposalHash, PermissionSnapshot string
		ExpectedRepairVersion, TargetVersion                                  uint64
	}{command.ClientRequestID, command.RepairID, command.Decision, command.ProposalHash, command.PermissionSnapshot, command.ExpectedRepairVersion, command.TargetVersion})
	input, descriptor, err := service.idempotencyInput(command.TenantID, command.UserID, repairDecisionOperation, command.IdempotencyKey, command.RequestID, canonical)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadRepairResponse(ctx, input, descriptor); loadErr != nil {
		return api.RepairResult{}, mapRepairServiceError(loadErr)
	} else if found {
		return result, nil
	}

	unlock, err := service.lockRepairDecision(ctx, command.RepairID)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	defer unlock()

	snapshot, err := service.loadRepairSnapshot(ctx, command.TenantID, command.RepairID)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.RepairResult{}, api.ErrResourceNotFound
	}
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	if snapshot.ProposalHash != command.ProposalHash || snapshot.TargetVersion != command.TargetVersion {
		return api.RepairResult{}, api.ErrStateConflict
	}
	if snapshot.InitiatorID == command.UserID {
		return api.RepairResult{}, api.ErrPermissionDenied
	}
	if snapshot.Version != command.ExpectedRepairVersion && snapshot.Status != "approved" && snapshot.Status != "executed" {
		return api.RepairResult{}, api.ErrVersionConflict
	}
	reauthenticatedAt, role, err := service.loadRepairSession(ctx, command.TenantID, command.UserID, command.SessionID)
	if err != nil || (role != "owner" && role != "admin") {
		return api.RepairResult{}, api.ErrReauthenticationRequired
	}
	now := service.now()
	if reauthenticatedAt.After(now) || now.Sub(reauthenticatedAt) > repairReauthenticationMaxAge {
		return api.RepairResult{}, api.ErrReauthenticationRequired
	}
	approvalID, err := ids.DeterministicUUID(service.IDKey, "repair-approval", input.RecordID)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	decisionEventID, _, _, err := repairDecisionEventIDs(service.Store.IDKey, approvalID)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	decisionPayloadValue := map[string]any{"subject_id": snapshot.ID, "subject_version": snapshot.Version + 1, "proposal_hash": snapshot.ProposalHash}
	if command.Decision == "approve" {
		decisionPayloadValue = map[string]any{"subject_id": approvalID, "subject_version": 1, "request_id": command.ClientRequestID, "proposal_hash": snapshot.ProposalHash, "approval_id": approvalID, "approver_user_id": command.UserID, "target_version": snapshot.TargetVersion, "permission_snapshot": command.PermissionSnapshot, "reauthenticated_at": reauthenticatedAt.UTC()}
	}
	decisionPayload, err := service.putJSON(ctx, command.TenantID, decisionEventID, repairEventClass, decisionPayloadValue)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	var approvedPayload PayloadPointer
	if command.Decision == "approve" {
		approvalEventIDs, loadErr := service.loadApprovalEventIDs(ctx, command.TenantID, command.RepairID)
		if loadErr != nil {
			return api.RepairResult{}, api.ErrDependencyUnavailable
		}
		approvalEventIDs = appendUnique(approvalEventIDs, decisionEventID)
		if len(approvalEventIDs) >= 2 {
			approvedEventID, _, _, idErr := repairApprovedEventIDs(service.Store.IDKey, command.RepairID)
			if idErr != nil {
				return api.RepairResult{}, api.ErrDependencyUnavailable
			}
			approvedPayload, err = service.putJSON(ctx, command.TenantID, approvedEventID, repairEventClass, map[string]any{"subject_id": snapshot.ID, "subject_version": snapshot.Version + 1, "command_id": nil, "proposal_hash": snapshot.ProposalHash, "repair_command_id": snapshot.ID, "target_kind": "tool_call", "target_id": snapshot.TargetID, "target_version": snapshot.TargetVersion, "approval_event_ids": approvalEventIDs})
			if err != nil {
				return api.RepairResult{}, api.ErrDependencyUnavailable
			}
		}
	}
	completed, err := service.beginRepairIdempotency(ctx, input)
	if err != nil {
		return api.RepairResult{}, mapRepairServiceError(err)
	}
	if completed {
		result, found, loadErr := service.loadRepairResponse(ctx, input, descriptor)
		if loadErr != nil || !found {
			return api.RepairResult{}, api.ErrDependencyUnavailable
		}
		return result, nil
	}
	decision, err := service.Store.DecideToolEffectRepair(ctx, DecideRepairCommand{
		RepairID: command.RepairID, TenantID: command.TenantID, ApproverUserID: command.UserID, SessionID: command.SessionID,
		ApprovalID: approvalID, Decision: command.Decision, ProposalHash: command.ProposalHash,
		ExpectedRepairVersion: command.ExpectedRepairVersion, ExpectedToolVersion: snapshot.TargetVersion, ExpectedEffectVersion: snapshot.EffectVersion,
		PermissionSnapshot: command.PermissionSnapshot, ReauthenticatedAt: reauthenticatedAt, Actor: repairActor(command.UserID, command.SessionID),
		CorrelationID: command.RequestID, DecisionEvent: decisionPayload, RepairApprovedEvent: approvedPayload,
	})
	if err != nil {
		return api.RepairResult{}, mapRepairServiceError(err)
	}
	if decision.Ready && decision.Status != "executed" {
		if _, err = service.executeApprovedRepair(ctx, command.TenantID, command.RepairID, command.RequestID); err != nil {
			return api.RepairResult{}, mapRepairServiceError(err)
		}
	}
	current, err := service.loadRepairSnapshot(ctx, command.TenantID, command.RepairID)
	if err != nil {
		return api.RepairResult{}, api.ErrDependencyUnavailable
	}
	result := api.RepairResult{ID: current.ID, Version: current.Version, Status: current.Status, UpdatedAt: current.UpdatedAt}
	return service.completeAndReadRepairResponse(ctx, input, descriptor, result)
}

func (service RepairControlService) validatePropose(command api.ProposeRepairCommand) error {
	if !service.valid() {
		return api.ErrDependencyUnavailable
	}
	validResolution := command.Resolution == "confirmed_occurred" || command.Resolution == "confirmed_not_occurred" || command.Resolution == "accepted_unknown"
	if command.RequestID == "" || command.ClientRequestID == "" || command.IdempotencyKey == "" || command.TenantID == "" || command.UserID == "" || command.SessionID == "" || command.TargetID == "" || command.TargetVersion == 0 || !validResolution || command.EvidenceHash == "" || command.Reason == "" {
		return api.ErrValidation
	}
	return nil
}

func (service RepairControlService) validateDecision(command api.DecideRepairCommand) error {
	if !service.valid() {
		return api.ErrDependencyUnavailable
	}
	if command.RequestID == "" || command.ClientRequestID == "" || command.IdempotencyKey == "" || command.TenantID == "" || command.UserID == "" || command.SessionID == "" || command.RepairID == "" || (command.Decision != "approve" && command.Decision != "reject") || command.ProposalHash == "" || command.ExpectedRepairVersion == 0 || command.TargetVersion == 0 || command.PermissionSnapshot == "" {
		return api.ErrValidation
	}
	return nil
}

func (service RepairControlService) valid() bool {
	queueValid := service.resumeQueueClass() == "interactive" || service.resumeQueueClass() == "background"
	return service.Pool != nil && service.Pool.Stat().MaxConns() >= 2 && service.Payloads != nil && service.Store.valid() && len(service.IDKey) >= 32 && bytes.Equal(service.IDKey, service.Store.IDKey) && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0 && service.proposalTTL() > 0 && service.proposalTTL() <= 24*time.Hour && queueValid && service.resumeResourceClass() != "" && service.resumePriority() >= 0 && service.resumePriority() <= 1000 && service.resumeCostUnits() > 0 && service.resumeCostUnits() <= 1_000_000_000_000 && service.resumeMaxAttempts() > 0 && service.resumeMaxAttempts() <= 100
}

func (service RepairControlService) now() time.Time {
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func (service RepairControlService) proposalTTL() time.Duration {
	if service.ProposalTTL > 0 {
		return service.ProposalTTL
	}
	return 30 * time.Minute
}

func (service RepairControlService) resumeQueueClass() string {
	if service.ResumeQueueClass != "" {
		return service.ResumeQueueClass
	}
	return "interactive"
}

func (service RepairControlService) resumeResourceClass() string {
	if service.ResumeResourceClass != "" {
		return service.ResumeResourceClass
	}
	return "llm"
}

func (service RepairControlService) resumePriority() int {
	if service.ResumePriority != 0 {
		return service.ResumePriority
	}
	return 50
}

func (service RepairControlService) resumeCostUnits() int64 {
	if service.ResumeCostUnits > 0 {
		return service.ResumeCostUnits
	}
	return 1
}

func (service RepairControlService) resumeMaxAttempts() int {
	if service.ResumeMaxAttempts > 0 {
		return service.ResumeMaxAttempts
	}
	return 5
}

func repairActor(userID, sessionID string) json.RawMessage {
	value, _ := json.Marshal(map[string]string{"kind": "user", "user_id": userID, "session_id": sessionID})
	return value
}

func mapRepairServiceError(err error) error {
	switch {
	case errors.Is(err, idempotency.ErrKeyConflict):
		return api.ErrIdempotencyConflict
	case errors.Is(err, ErrRepairAuthorization):
		return api.ErrReauthenticationRequired
	case errors.Is(err, ErrRepairNotActionable), errors.Is(err, ErrRepairConflict):
		return api.ErrStateConflict
	case errors.Is(err, ErrInvalidCommand):
		return api.ErrValidation
	default:
		return api.ErrDependencyUnavailable
	}
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

var _ api.RepairService = RepairControlService{}
