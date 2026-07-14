package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionapi "github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	claimRepairProposalOperation = "claim_repair.propose"
	claimRepairDecisionOperation = "claim_repair.decide"
	claimRepairResponseClass     = "identity-idempotency"
	claimRepairEvidenceClass     = "repair-evidence"
	claimRepairEventClass        = "event-payload"
	claimRepairCommandClass      = "anonymous-claim-command"
	claimRepairReauthMaxAge      = 5 * time.Minute
)

var (
	errClaimRepairAuthorization = errors.New("anonymous claim repair is not authorized")
	errClaimRepairConflict      = errors.New("anonymous claim repair conflicts with durable state")
	errClaimRepairNotActionable = errors.New("anonymous claim repair is not actionable")
)

// AnonymousClaimRepairControlService is the identity-domain implementation of
// the shared Repair API. It restores only the state independently derived by
// AnonymousClaimRepairInspector and resumes the ordinary claim saga by outbox.
type AnonymousClaimRepairControlService struct {
	Pool                 *pgxpool.Pool
	ClaimStore           AnonymousClaimStore
	Inspector            AnonymousClaimRepairInspector
	Payloads             payload.Store
	Appender             eventpostgres.Appender
	Epochs               eventpostgres.EpochAuthority
	SystemTenantID       string
	StoreEpoch           string
	IDKey                []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	IdempotencyTTL       time.Duration
	ProposalTTL          time.Duration
	Now                  func() time.Time
}

type claimRepairSnapshot struct {
	ID, Status, Resolution, ProposalHash, EvidenceHash          string
	SourceTenantID, ClaimID, ClaimKey, InitiatorID, EvidenceRef string
	Version, ClaimVersion                                       uint64
	UpdatedAt, ExpiresAt                                        time.Time
}

type claimRepairIdempotency struct {
	RecordID, TenantID, UserID, OperationID string
	RawKey, RequestHash, RequestID          string
}

type claimRepairPointer struct{ Ref, Hash string }

func (service AnonymousClaimRepairControlService) ProposeRepair(ctx context.Context, command executionapi.ProposeRepairCommand) (result executionapi.RepairResult, resultErr error) {
	if err := service.validateProposal(command); err != nil {
		return executionapi.RepairResult{}, err
	}
	canonical, _ := json.Marshal(struct {
		ClientRequestID, ClaimID, Resolution, EvidenceHash, Reason string
		ClaimVersion                                               uint64
	}{command.ClientRequestID, command.TargetID, command.Resolution, command.EvidenceHash, command.Reason, command.TargetVersion})
	input, descriptor, err := service.claimRepairIdempotencyInput(command.TenantID, command.UserID, claimRepairProposalOperation, command.IdempotencyKey, command.RequestID, canonical)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadClaimRepairResponse(ctx, input, descriptor); loadErr != nil {
		return executionapi.RepairResult{}, mapClaimRepairError(loadErr)
	} else if found {
		return result, nil
	}
	saga, err := service.ClaimStore.Load(ctx, command.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionapi.RepairResult{}, executionapi.ErrResourceNotFound
	}
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	if saga.Status != anonymousclaim.ManualReview || saga.TargetTenantID != command.TenantID {
		return executionapi.RepairResult{}, executionapi.ErrStateConflict
	}
	if saga.Version != command.TargetVersion {
		return executionapi.RepairResult{}, executionapi.ErrVersionConflict
	}
	resolution, err := service.Inspector.InspectManualReview(ctx, saga)
	if err != nil {
		return executionapi.RepairResult{}, mapClaimRepairError(err)
	}
	if claimRepairResolutionForStatus(resolution.TargetStatus) != command.Resolution || resolution.EvidenceHash != command.EvidenceHash {
		return executionapi.RepairResult{}, executionapi.ErrStateConflict
	}
	repairID, err := ids.DeterministicUUID(service.IDKey, "anonymous-claim-repair", input.RecordID)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	reasonHash, err := idempotency.RequestDigest([]byte(command.Reason), service.RequestDigestPepper)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	proposalCanonical, _ := json.Marshal(struct {
		RepairID, SourceTenantID, TargetTenantID, ClaimID, ClaimKey string
		Resolution, EvidenceHash, ReasonHash                        string
		ClaimVersion                                                uint64
	}{repairID, service.SystemTenantID, command.TenantID, saga.ID, saga.ClaimKey, command.Resolution, resolution.EvidenceHash, reasonHash, saga.Version})
	proposalHash, err := idempotency.RequestDigest(proposalCanonical, service.RequestDigestPepper)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	reasonPayload, err := service.putClaimRepairJSON(ctx, command.TenantID, repairID, claimRepairEvidenceClass, map[string]any{"repair_command_id": repairID, "claim_id": saga.ID, "claim_key": saga.ClaimKey, "evidence_hash": resolution.EvidenceHash, "reason": command.Reason, "resolution": command.Resolution})
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	proposalIDs, err := service.claimRepairIDs("proposal", repairID)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	proposalPayload, err := service.putClaimRepairJSON(ctx, command.TenantID, proposalIDs.event, claimRepairEventClass, map[string]any{"subject_id": repairID, "subject_version": 1, "request_id": command.ClientRequestID, "command_id": nil, "proposal_hash": proposalHash})
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	completed, err := service.beginClaimRepairIdempotency(ctx, input)
	if err != nil {
		return executionapi.RepairResult{}, mapClaimRepairError(err)
	}
	if completed {
		return service.mustLoadClaimRepairResponse(ctx, input, descriptor)
	}
	finished := false
	defer func() {
		if !finished && resultErr != nil {
			_ = service.abandonClaimRepairIdempotency(context.Background(), input)
		}
	}()
	now := service.now()
	created, err := service.proposeAnonymousClaimRepair(ctx, proposeAnonymousClaimRepair{
		RepairID: repairID, TargetTenantID: command.TenantID, SourceTenantID: service.SystemTenantID,
		InitiatorUserID: command.UserID, InitiatorSessionID: command.SessionID, TargetUserID: saga.TargetUserID,
		ClaimID: saga.ID, ClaimKey: saga.ClaimKey, ClaimVersion: saga.Version, Resolution: command.Resolution,
		ProposalHash: proposalHash, EvidenceHash: resolution.EvidenceHash, EvidenceRef: reasonPayload.Ref,
		ExpiresAt: now.Add(service.proposalTTL()), Actor: claimRepairActor(command.UserID, command.SessionID),
		CorrelationID: command.RequestID, Event: proposalPayload, IDs: proposalIDs,
	})
	if err != nil {
		return executionapi.RepairResult{}, mapClaimRepairError(err)
	}
	result = executionapi.RepairResult{ID: created.ID, Version: created.Version, Status: created.Status, UpdatedAt: created.UpdatedAt}
	result, resultErr = service.completeClaimRepairResponse(ctx, input, descriptor, result)
	finished = resultErr == nil
	return result, resultErr
}

func (service AnonymousClaimRepairControlService) DecideRepair(ctx context.Context, command executionapi.DecideRepairCommand) (result executionapi.RepairResult, resultErr error) {
	if err := service.validateDecision(command); err != nil {
		return executionapi.RepairResult{}, err
	}
	canonical, _ := json.Marshal(struct {
		ClientRequestID, RepairID, Decision, ProposalHash, PermissionSnapshot string
		RepairVersion, ClaimVersion                                           uint64
	}{command.ClientRequestID, command.RepairID, command.Decision, command.ProposalHash, command.PermissionSnapshot, command.ExpectedRepairVersion, command.TargetVersion})
	input, descriptor, err := service.claimRepairIdempotencyInput(command.TenantID, command.UserID, claimRepairDecisionOperation, command.IdempotencyKey, command.RequestID, canonical)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadClaimRepairResponse(ctx, input, descriptor); loadErr != nil {
		return executionapi.RepairResult{}, mapClaimRepairError(loadErr)
	} else if found {
		return result, nil
	}
	unlock, err := service.lockClaimRepair(ctx, command.RepairID)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	defer unlock()
	snapshot, err := service.loadClaimRepairSnapshot(ctx, command.TenantID, command.RepairID)
	if errors.Is(err, pgx.ErrNoRows) {
		return executionapi.RepairResult{}, executionapi.ErrResourceNotFound
	}
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	if snapshot.ProposalHash != command.ProposalHash || snapshot.ClaimVersion != command.TargetVersion {
		return executionapi.RepairResult{}, executionapi.ErrStateConflict
	}
	if snapshot.InitiatorID == command.UserID {
		return executionapi.RepairResult{}, executionapi.ErrPermissionDenied
	}
	if snapshot.Version != command.ExpectedRepairVersion && snapshot.Status != "approved" && snapshot.Status != "executed" {
		return executionapi.RepairResult{}, executionapi.ErrVersionConflict
	}
	reauthenticatedAt, role, err := service.loadClaimRepairSession(ctx, command.TenantID, command.UserID, command.SessionID)
	if err != nil || (role != "owner" && role != "admin") || reauthenticatedAt.After(service.now()) || service.now().Sub(reauthenticatedAt) > claimRepairReauthMaxAge {
		return executionapi.RepairResult{}, executionapi.ErrReauthenticationRequired
	}
	approvalID, err := ids.DeterministicUUID(service.IDKey, "anonymous-claim-repair-approval", input.RecordID)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	decisionIDs, err := service.claimRepairIDs("decision", approvalID)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	decisionType := "ApprovalGranted"
	aggregateID, aggregateKind, aggregateVersion := approvalID, "repair_approval", uint64(1)
	if command.Decision == "reject" {
		decisionType, aggregateID, aggregateKind, aggregateVersion = "ApprovalRejected", snapshot.ID, "repair_command", snapshot.Version+1
	}
	decisionPayload, err := service.putClaimRepairJSON(ctx, command.TenantID, decisionIDs.event, claimRepairEventClass, map[string]any{"subject_id": aggregateID, "subject_version": aggregateVersion, "request_id": command.ClientRequestID, "proposal_hash": snapshot.ProposalHash, "approval_id": approvalID, "approver_user_id": command.UserID, "target_version": snapshot.ClaimVersion, "permission_snapshot": command.PermissionSnapshot, "reauthenticated_at": reauthenticatedAt.UTC()})
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	approvalEventIDs, err := service.loadClaimRepairApprovalEventIDs(ctx, command.TenantID, command.RepairID)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	approvalEventIDs = appendClaimRepairUnique(approvalEventIDs, decisionIDs.event)
	approvedIDs, err := service.claimRepairIDs("approved", command.RepairID)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	var approvedPayload claimRepairPointer
	if command.Decision == "approve" && len(approvalEventIDs) >= 2 {
		approvedPayload, err = service.putClaimRepairJSON(ctx, command.TenantID, approvedIDs.event, claimRepairEventClass, map[string]any{"subject_id": snapshot.ID, "subject_version": snapshot.Version + 1, "command_id": nil, "proposal_hash": snapshot.ProposalHash, "repair_command_id": snapshot.ID, "target_kind": "anonymous_claim", "target_id": snapshot.ClaimID, "target_version": snapshot.ClaimVersion, "approval_event_ids": approvalEventIDs})
		if err != nil {
			return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
		}
	}
	completed, err := service.beginClaimRepairIdempotency(ctx, input)
	if err != nil {
		return executionapi.RepairResult{}, mapClaimRepairError(err)
	}
	if completed {
		return service.mustLoadClaimRepairResponse(ctx, input, descriptor)
	}
	finished := false
	defer func() {
		if !finished && resultErr != nil {
			_ = service.abandonClaimRepairIdempotency(context.Background(), input)
		}
	}()
	decision, err := service.decideAnonymousClaimRepair(ctx, decideAnonymousClaimRepair{
		RepairID: command.RepairID, TargetTenantID: command.TenantID, ApproverUserID: command.UserID,
		SessionID: command.SessionID, ApprovalID: approvalID, Decision: command.Decision,
		ProposalHash: command.ProposalHash, ExpectedRepairVersion: command.ExpectedRepairVersion,
		ExpectedClaimVersion: command.TargetVersion, PermissionSnapshot: command.PermissionSnapshot,
		ReauthenticatedAt: reauthenticatedAt, Actor: claimRepairActor(command.UserID, command.SessionID),
		CorrelationID: command.RequestID, DecisionType: decisionType, DecisionAggregateKind: aggregateKind,
		DecisionAggregateID: aggregateID, DecisionAggregateVersion: aggregateVersion, DecisionEvent: decisionPayload,
		DecisionIDs: decisionIDs, ApprovedEvent: approvedPayload, ApprovedIDs: approvedIDs,
	})
	if err != nil {
		return executionapi.RepairResult{}, mapClaimRepairError(err)
	}
	if decision.Ready && decision.Status != "executed" {
		if _, err = service.executeAnonymousClaimRepair(ctx, command.TenantID, command.RepairID, command.RequestID, claimRepairActor(command.UserID, command.SessionID)); err != nil {
			return executionapi.RepairResult{}, mapClaimRepairError(err)
		}
	}
	current, err := service.loadClaimRepairSnapshot(ctx, command.TenantID, command.RepairID)
	if err != nil {
		return executionapi.RepairResult{}, executionapi.ErrDependencyUnavailable
	}
	result = executionapi.RepairResult{ID: current.ID, Version: current.Version, Status: current.Status, UpdatedAt: current.UpdatedAt}
	result, resultErr = service.completeClaimRepairResponse(ctx, input, descriptor, result)
	finished = resultErr == nil
	return result, resultErr
}

func (service AnonymousClaimRepairControlService) validateProposal(command executionapi.ProposeRepairCommand) error {
	if !service.valid() {
		return executionapi.ErrDependencyUnavailable
	}
	if command.RequestID == "" || command.ClientRequestID == "" || command.IdempotencyKey == "" || command.TenantID == "" || command.UserID == "" || command.SessionID == "" || command.TargetID == "" || command.TargetVersion == 0 || !isClaimRepairResolution(command.Resolution) || command.EvidenceHash == "" || command.Reason == "" {
		return executionapi.ErrValidation
	}
	return nil
}

func (service AnonymousClaimRepairControlService) validateDecision(command executionapi.DecideRepairCommand) error {
	if !service.valid() {
		return executionapi.ErrDependencyUnavailable
	}
	if command.RequestID == "" || command.ClientRequestID == "" || command.IdempotencyKey == "" || command.TenantID == "" || command.UserID == "" || command.SessionID == "" || command.RepairID == "" || (command.Decision != "approve" && command.Decision != "reject") || command.ProposalHash == "" || command.ExpectedRepairVersion == 0 || command.TargetVersion == 0 || command.PermissionSnapshot == "" {
		return executionapi.ErrValidation
	}
	return nil
}

func (service AnonymousClaimRepairControlService) valid() bool {
	return service.Pool != nil && service.Pool.Stat().MaxConns() >= 2 && service.Payloads != nil && service.Epochs != nil && service.SystemTenantID != "" && service.StoreEpoch != "" && service.ClaimStore.Pool == service.Pool && service.ClaimStore.SystemTenantID == service.SystemTenantID && service.Inspector.Pool == service.Pool && len(service.IDKey) >= 32 && len(service.Inspector.IdentityKey) >= 32 && bytes.Equal(service.Inspector.IdentityKey, service.ClaimStore.IdentityKey) && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0 && service.proposalTTL() > 0 && service.proposalTTL() <= 24*time.Hour
}

func (service AnonymousClaimRepairControlService) now() time.Time {
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func (service AnonymousClaimRepairControlService) proposalTTL() time.Duration {
	if service.ProposalTTL > 0 {
		return service.ProposalTTL
	}
	return 30 * time.Minute
}

func (service AnonymousClaimRepairControlService) requireCurrentEpoch(ctx context.Context) error {
	epoch, err := service.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch != service.StoreEpoch {
		return errClaimRepairNotActionable
	}
	return nil
}

func isClaimRepairResolution(value string) bool {
	return value == "reconcile_reserved" || value == "reconcile_destination_committed" || value == "reconcile_erasing"
}

func claimRepairResolutionForStatus(status string) string { return "reconcile_" + status }

func claimRepairActor(userID, sessionID string) json.RawMessage {
	value, _ := json.Marshal(map[string]string{"kind": "user", "user_id": userID, "session_id": sessionID})
	return value
}

func mapClaimRepairError(err error) error {
	switch {
	case errors.Is(err, idempotency.ErrKeyConflict):
		return executionapi.ErrIdempotencyConflict
	case errors.Is(err, errClaimRepairAuthorization):
		return executionapi.ErrReauthenticationRequired
	case errors.Is(err, anonymousclaim.ErrVersionConflict):
		return executionapi.ErrVersionConflict
	case errors.Is(err, ErrAnonymousClaimRepairUnresolved), errors.Is(err, errClaimRepairConflict), errors.Is(err, errClaimRepairNotActionable), errors.Is(err, anonymousclaim.ErrInvariant), errors.Is(err, anonymousclaim.ErrInvalidTransition):
		return executionapi.ErrStateConflict
	default:
		return fmt.Errorf("%w: %v", executionapi.ErrDependencyUnavailable, err)
	}
}

func appendClaimRepairUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

var _ executionapi.RepairService = AnonymousClaimRepairControlService{}
