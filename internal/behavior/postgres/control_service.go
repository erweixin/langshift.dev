package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	behaviorapi "github.com/langshift/lites/internal/behavior/api"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	createSnapshotOperation   = "behavior.snapshots.create"
	recordEvaluationOperation = "behavior.evaluations.record"
	promoteOperation          = "behavior.promotions.create"
	rollbackOperation         = "behavior.rollbacks.automatic"
	behaviorResponseClass     = "behavior-control-idempotency"
	behaviorEventClass        = "event-payload"
)

// ControlService turns authenticated release-control commands into encrypted,
// idempotent EventStore mutations. The immutable Store remains the final
// cryptographic and database consistency boundary.
type ControlService struct {
	Pool                 *pgxpool.Pool
	Store                Store
	Payloads             payload.Store
	IDKey                []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	IdempotencyTTL       time.Duration
	ReauthenticationAge  time.Duration
	AutomationUserID     string
	Now                  func() time.Time
}

type behaviorIdempotencyInput struct {
	RecordID, TenantID, UserID, OperationID string
	RawKey, RequestHash, RequestID          string
}

type behaviorIdentifiers struct {
	ResourceID, EventID, OutboxID, PublishID string
}

var controlUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (service ControlService) CreateSnapshot(ctx context.Context, command behaviorapi.CreateSnapshotCommand) (behaviorapi.MutationResult, error) {
	canonical, err := command.Manifest.Canonical()
	if err != nil || !service.validAdminMetadata(command.CommandMetadata) {
		return behaviorapi.MutationResult{}, behaviorapi.ErrValidation
	}
	canonicalRequest, _ := json.Marshal(struct {
		ClientRequestID string            `json:"request_id"`
		Manifest        behavior.Manifest `json:"manifest"`
	}{command.ClientRequestID, canonical})
	input, descriptor, err := service.idempotencyInput(command.TenantID, command.UserID, createSnapshotOperation, command.IdempotencyKey, command.RequestID, canonicalRequest)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadResponse(ctx, input, descriptor); loadErr != nil {
		return behaviorapi.MutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	completed, err := service.beginIdempotency(ctx, input)
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	if completed {
		return service.mustLoadResponse(ctx, input, descriptor)
	}
	identifiers, err := service.identifiers(createSnapshotOperation, input.RecordID)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	snapshotID, _ := canonical.SnapshotID()
	manifestHash, _ := canonical.Hash()
	eventPayload, err := service.prepareEventPayload(ctx, input, identifiers.EventID, map[string]any{
		"snapshot_id": snapshotID, "profile": canonical.Profile, "manifest_hash": manifestHash,
		"source_commit": canonical.SourceCommit, "created_at": canonical.CreatedAt,
	})
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	stored, err := service.Store.CreateSnapshot(ctx, CreateSnapshotCommand{
		ID: identifiers.ResourceID, EventID: identifiers.EventID, OutboxID: identifiers.OutboxID, PublishCommandID: identifiers.PublishID,
		TenantID: command.TenantID, UserID: command.UserID, CorrelationID: command.RequestID, Manifest: canonical,
		Actor: adminActor(command.UserID, command.SessionID), EventPayload: eventPayload,
	})
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	result := behaviorapi.MutationResult{ID: stored.ID, ResourceID: snapshotID, EventID: stored.EventID, Hash: stored.Hash, Replayed: stored.Replayed}
	return service.completeAndReadResponse(ctx, input, descriptor, result)
}

func (service ControlService) RecordEvaluation(ctx context.Context, command behaviorapi.RecordEvaluationCommand) (behaviorapi.MutationResult, error) {
	if !service.validAdminMetadata(command.CommandMetadata) {
		return behaviorapi.MutationResult{}, behaviorapi.ErrValidation
	}
	reportHash, err := command.Report.Hash()
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrValidation
	}
	canonicalRequest, _ := json.Marshal(struct {
		ClientRequestID string                    `json:"request_id"`
		Report          behavior.EvaluationReport `json:"report"`
	}{command.ClientRequestID, command.Report})
	input, descriptor, err := service.idempotencyInput(command.TenantID, command.UserID, recordEvaluationOperation, command.IdempotencyKey, command.RequestID, canonicalRequest)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadResponse(ctx, input, descriptor); loadErr != nil {
		return behaviorapi.MutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	completed, err := service.beginIdempotency(ctx, input)
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	if completed {
		return service.mustLoadResponse(ctx, input, descriptor)
	}
	identifiers, err := service.identifiers(recordEvaluationOperation, input.RecordID)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	eventPayload, err := service.prepareEventPayload(ctx, input, identifiers.EventID, map[string]any{
		"report_id": command.Report.ReportID, "profile": command.Report.Profile,
		"candidate_snapshot_id": command.Report.CandidateSnapshotID, "baseline_snapshot_id": command.Report.BaselineSnapshotID,
		"report_hash": reportHash, "passed": command.Report.Evaluate().Passed, "evaluated_at": command.Report.EvaluatedAt,
	})
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	stored, err := service.Store.RecordEvaluation(ctx, RecordEvaluationCommand{
		ID: identifiers.ResourceID, EventID: identifiers.EventID, OutboxID: identifiers.OutboxID, PublishCommandID: identifiers.PublishID,
		TenantID: command.TenantID, UserID: command.UserID, CorrelationID: command.RequestID, Report: command.Report,
		Actor: adminActor(command.UserID, command.SessionID), EventPayload: eventPayload,
	})
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	result := behaviorapi.MutationResult{ID: stored.ID, ResourceID: command.Report.ReportID, EventID: stored.EventID, Hash: stored.Hash, Replayed: stored.Replayed}
	return service.completeAndReadResponse(ctx, input, descriptor, result)
}

func (service ControlService) Promote(ctx context.Context, command behaviorapi.PromoteCommand) (behaviorapi.MutationResult, error) {
	if !service.validAdminMetadata(command.CommandMetadata) || command.Request.TenantID != command.TenantID {
		return behaviorapi.MutationResult{}, behaviorapi.ErrValidation
	}
	canonicalRequest, _ := json.Marshal(struct {
		ClientRequestID string                    `json:"request_id"`
		Promotion       behavior.PromotionRequest `json:"promotion"`
	}{command.ClientRequestID, command.Request})
	input, descriptor, err := service.idempotencyInput(command.TenantID, command.UserID, promoteOperation, command.IdempotencyKey, command.RequestID, canonicalRequest)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadResponse(ctx, input, descriptor); loadErr != nil {
		return behaviorapi.MutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	if err = service.requireRecentAdminSession(ctx, command.TenantID, command.UserID, command.SessionID); err != nil {
		return behaviorapi.MutationResult{}, err
	}
	manifest, report, evaluationID, err := service.loadPromotionEvidence(ctx, command.TenantID, command.Request)
	if errors.Is(err, pgx.ErrNoRows) {
		return behaviorapi.MutationResult{}, behaviorapi.ErrResourceNotFound
	}
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	if _, err = service.Store.ValidatePromotion(manifest, report, command.Request); err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	completed, err := service.beginIdempotency(ctx, input)
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	if completed {
		return service.mustLoadResponse(ctx, input, descriptor)
	}
	identifiers, err := service.identifiers(promoteOperation, input.RecordID)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	channelID, err := service.channelID(command.TenantID, command.Request.Profile, command.Request.Environment)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	promotionHash := ""
	if _, promotionHash, err = command.Request.SigningPayload(); err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrValidation
	}
	eventPayload, err := service.prepareEventPayload(ctx, input, identifiers.EventID, map[string]any{
		"channel_id": channelID, "profile": command.Request.Profile, "environment": command.Request.Environment,
		"sequence": command.Request.Sequence, "candidate_snapshot_id": command.Request.CandidateSnapshotID,
		"previous_snapshot_id": command.Request.PreviousSnapshotID, "manifest_hash": command.Request.ManifestHash,
		"evaluation_report_id": evaluationID, "evaluation_report_hash": command.Request.EvaluationReportHash,
		"promotion_hash": promotionHash, "rollout": command.Request.Rollout, "auto_rollback": command.Request.AutoRollback,
		"approvals": command.Request.Approvals, "activated_at": service.now(),
	})
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	stored, err := service.Store.Promote(ctx, PromoteCommand{
		ID: identifiers.ResourceID, ChannelID: channelID, EventID: identifiers.EventID, OutboxID: identifiers.OutboxID,
		PublishCommandID: identifiers.PublishID, EvaluationID: evaluationID, TenantID: command.TenantID, UserID: command.UserID,
		CorrelationID: command.RequestID, Manifest: manifest, Report: report, Request: command.Request,
		Actor: adminActor(command.UserID, command.SessionID), EventPayload: eventPayload,
	})
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	result := behaviorapi.MutationResult{ID: stored.ID, ResourceID: command.Request.CandidateSnapshotID, EventID: stored.EventID, Hash: stored.Hash, Sequence: stored.Sequence, Replayed: stored.Replayed}
	return service.completeAndReadResponse(ctx, input, descriptor, result)
}

func (service ControlService) AutomaticRollback(ctx context.Context, command behaviorapi.AutomaticRollbackCommand) (behaviorapi.MutationResult, error) {
	request := command.Request
	if !service.valid() || !controlUUIDPattern.MatchString(service.AutomationUserID) || !controlUUIDPattern.MatchString(command.RequestID) || command.ClientRequestID == "" || command.IdempotencyKey == "" || request.TenantID == "" {
		return behaviorapi.MutationResult{}, behaviorapi.ErrValidation
	}
	canonicalRequest, _ := json.Marshal(struct {
		ClientRequestID string                   `json:"request_id"`
		Rollback        behavior.RollbackRequest `json:"rollback"`
	}{command.ClientRequestID, request})
	input, descriptor, err := service.idempotencyInput(request.TenantID, service.AutomationUserID, rollbackOperation, command.IdempotencyKey, command.RequestID, canonicalRequest)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	if result, found, loadErr := service.loadResponse(ctx, input, descriptor); loadErr != nil {
		return behaviorapi.MutationResult{}, service.mapError(loadErr)
	} else if found {
		return result, nil
	}
	if _, err = service.Store.ValidateRollback(request); err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	completed, err := service.beginIdempotency(ctx, input)
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	if completed {
		return service.mustLoadResponse(ctx, input, descriptor)
	}
	identifiers, err := service.identifiers(rollbackOperation, input.RecordID)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	channelID, err := service.channelID(request.TenantID, request.Profile, request.Environment)
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrDependencyUnavailable
	}
	_, rollbackHash, err := request.SigningPayload()
	if err != nil {
		return behaviorapi.MutationResult{}, behaviorapi.ErrValidation
	}
	eventPayload, err := service.prepareEventPayload(ctx, input, identifiers.EventID, map[string]any{
		"channel_id": channelID, "profile": request.Profile, "environment": request.Environment, "sequence": request.Sequence,
		"from_snapshot_id": request.FromSnapshotID, "to_snapshot_id": request.ToSnapshotID, "rollback_hash": rollbackHash,
		"trigger": request.Trigger, "observed_value": request.ObservedValue, "threshold": request.Threshold,
		"incident_evidence_hash": request.IncidentEvidenceHash, "automation_key_id": request.AutomationKeyID,
		"automation_signature": request.Signature, "triggered_at": request.OccurredAt, "activated_at": service.now(),
	})
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	stored, err := service.Store.Rollback(ctx, RollbackCommand{
		ID: identifiers.ResourceID, ChannelID: channelID, EventID: identifiers.EventID, OutboxID: identifiers.OutboxID,
		PublishCommandID: identifiers.PublishID, TenantID: request.TenantID, UserID: service.AutomationUserID,
		CorrelationID: command.RequestID, Request: request, Actor: automationActor(request.AutomationKeyID), EventPayload: eventPayload,
	})
	if err != nil {
		return behaviorapi.MutationResult{}, service.mapError(err)
	}
	result := behaviorapi.MutationResult{ID: stored.ID, ResourceID: request.ToSnapshotID, EventID: stored.EventID, Hash: stored.Hash, Sequence: stored.Sequence, Replayed: stored.Replayed}
	return service.completeAndReadResponse(ctx, input, descriptor, result)
}

func (service ControlService) Current(ctx context.Context, tenantID string, profile behavior.Profile, environment string) (behavior.ChannelBinding, error) {
	if !service.valid() || tenantID == "" || !profile.Valid() || environment != "staging" && environment != "production" {
		return behavior.ChannelBinding{}, behaviorapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return behavior.ChannelBinding{}, behaviorapi.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return behavior.ChannelBinding{}, behaviorapi.ErrDependencyUnavailable
	}
	result, err := service.Store.ResolveCurrent(ctx, tx, tenantID, profile, environment)
	if errors.Is(err, ErrNoActiveChannel) {
		return behavior.ChannelBinding{}, behaviorapi.ErrResourceNotFound
	}
	if err != nil {
		return behavior.ChannelBinding{}, behaviorapi.ErrDependencyUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return behavior.ChannelBinding{}, behaviorapi.ErrDependencyUnavailable
	}
	return result, nil
}

func (service ControlService) loadPromotionEvidence(ctx context.Context, tenantID string, request behavior.PromotionRequest) (behavior.Manifest, behavior.EvaluationReport, string, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return behavior.Manifest{}, behavior.EvaluationReport{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return behavior.Manifest{}, behavior.EvaluationReport{}, "", err
	}
	var manifestJSON, reportJSON []byte
	var evaluationID string
	if err = tx.QueryRow(ctx, `SELECT manifest FROM agent.behavior_snapshots WHERE tenant_id=$1 AND snapshot_id=$2 AND manifest_hash=$3 AND profile_name=$4`, tenantID, request.CandidateSnapshotID, request.ManifestHash, request.Profile).Scan(&manifestJSON); err != nil {
		return behavior.Manifest{}, behavior.EvaluationReport{}, "", err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text,report FROM agent.behavior_evaluation_reports WHERE tenant_id=$1 AND candidate_snapshot_id=$2 AND baseline_snapshot_id=$3 AND report_hash=$4 AND profile_name=$5 AND passed`, tenantID, request.CandidateSnapshotID, request.PreviousSnapshotID, request.EvaluationReportHash, request.Profile).Scan(&evaluationID, &reportJSON); err != nil {
		return behavior.Manifest{}, behavior.EvaluationReport{}, "", err
	}
	var manifest behavior.Manifest
	var report behavior.EvaluationReport
	if json.Unmarshal(manifestJSON, &manifest) != nil || json.Unmarshal(reportJSON, &report) != nil {
		return behavior.Manifest{}, behavior.EvaluationReport{}, "", errors.New("invalid persisted behavior evidence")
	}
	if err = tx.Commit(ctx); err != nil {
		return behavior.Manifest{}, behavior.EvaluationReport{}, "", err
	}
	return manifest, report, evaluationID, nil
}

func (service ControlService) requireRecentAdminSession(ctx context.Context, tenantID, userID, sessionID string) error {
	if service.ReauthenticationAge <= 0 || service.ReauthenticationAge > time.Hour {
		return behaviorapi.ErrDependencyUnavailable
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return behaviorapi.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return behaviorapi.ErrDependencyUnavailable
	}
	var reauthenticatedAt time.Time
	var role string
	err = tx.QueryRow(ctx, `SELECT s.reauthenticated_at,m.role FROM identity.sessions s JOIN identity.memberships m ON m.tenant_id=s.active_tenant_id AND m.user_id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active_tenant_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$4 AND s.reauthenticated_at IS NOT NULL AND m.status='active'`, sessionID, userID, tenantID, service.now()).Scan(&reauthenticatedAt, &role)
	if err != nil || role != "owner" && role != "admin" || service.now().Sub(reauthenticatedAt) > service.ReauthenticationAge {
		return behaviorapi.ErrReauthentication
	}
	if err = tx.Commit(ctx); err != nil {
		return behaviorapi.ErrDependencyUnavailable
	}
	return nil
}

func (service ControlService) identifiers(operation, seed string) (behaviorIdentifiers, error) {
	var result behaviorIdentifiers
	for domain, target := range map[string]*string{
		"resource": &result.ResourceID, "event": &result.EventID, "outbox": &result.OutboxID, "publish": &result.PublishID,
	} {
		value, err := ids.DeterministicUUID(service.IDKey, operation+":"+domain, seed)
		if err != nil {
			return behaviorIdentifiers{}, err
		}
		*target = value
	}
	return result, nil
}

func (service ControlService) channelID(tenantID string, profile behavior.Profile, environment string) (string, error) {
	return ids.DeterministicUUID(service.IDKey, "behavior-channel", tenantID+"\x00"+string(profile)+"\x00"+environment)
}

func (service ControlService) validAdminMetadata(metadata behaviorapi.CommandMetadata) bool {
	return service.valid() && controlUUIDPattern.MatchString(metadata.RequestID) && metadata.ClientRequestID != "" && metadata.IdempotencyKey != "" && controlUUIDPattern.MatchString(metadata.TenantID) && controlUUIDPattern.MatchString(metadata.UserID) && controlUUIDPattern.MatchString(metadata.SessionID)
}

func (service ControlService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0 && service.Now != nil
}

func (service ControlService) now() time.Time { return service.Now().UTC().Truncate(time.Microsecond) }

func (service ControlService) mapError(err error) error {
	switch {
	case errors.Is(err, idempotency.ErrKeyConflict):
		return errors.Join(behaviorapi.ErrIdempotencyConflict, err)
	case errors.Is(err, ErrConflict):
		return errors.Join(behaviorapi.ErrStateConflict, err)
	case errors.Is(err, ErrCommand), errors.Is(err, behavior.ErrAuthorization), errors.Is(err, behavior.ErrInvalidManifest), errors.Is(err, behavior.ErrInvalidReport):
		return errors.Join(behaviorapi.ErrValidation, err)
	default:
		return errors.Join(behaviorapi.ErrDependencyUnavailable, err)
	}
}

func adminActor(userID, sessionID string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]string{"kind": "user", "user_id": userID, "session_id": sessionID, "component": "behavior-control-plane"})
	return encoded
}

func automationActor(keyID string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]string{"kind": "system", "key_id": keyID, "component": "behavior-rollback-controller"})
	return encoded
}

var _ behaviorapi.ControlService = ControlService{}
var _ behaviorapi.RollbackService = ControlService{}
