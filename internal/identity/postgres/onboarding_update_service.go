package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

type onboardingUpdateSnapshot struct {
	CurrentRole       string `json:"current_role"`
	TargetRole        string `json:"target_role"`
	ExperienceSummary string `json:"experience_summary"`
	WeeklyMinutes     int    `json:"weekly_minutes"`
}

type onboardingUpdatePrepared struct {
	EventID, OutboxID, PublishID string
	CurrentRoleInput             json.RawMessage
	TargetRoleInput              json.RawMessage
	ExperienceManifest           string
	EventPayload                 payload.Manifest
	ResponsePayload              payload.Manifest
	Result                       api.OnboardingUpdateResult
}

func (service OnboardingService) UpdateOnboarding(ctx context.Context, command api.OnboardingUpdateCommand) (api.OnboardingUpdateResult, error) {
	if err := service.validateUpdate(command); err != nil {
		return api.OnboardingUpdateResult{}, err
	}
	canonical, err := json.Marshal(command)
	if err != nil {
		return api.OnboardingUpdateResult{}, api.ErrDependencyUnavailable
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.OnboardingUpdateResult{}, api.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IdentityKey, "idempotency-record:onboarding.update", command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return api.OnboardingUpdateResult{}, api.ErrDependencyUnavailable
	}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "onboarding.update"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	executor := IdempotencyExecutor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return api.OnboardingUpdateResult{}, mapIdentityError(loadErr)
	} else if found {
		return service.readOnboardingUpdateResponse(ctx, descriptor, response)
	}
	snapshot, err := service.loadOnboardingUpdateSnapshot(ctx, command)
	if err != nil {
		return api.OnboardingUpdateResult{}, err
	}
	prepared, err := service.prepareOnboardingUpdate(ctx, command, recordID, descriptor, snapshot)
	if err != nil {
		return api.OnboardingUpdateResult{}, err
	}
	response, _, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		return service.commitOnboardingUpdate(ctx, tx, command, prepared)
	})
	if err != nil {
		return api.OnboardingUpdateResult{}, mapIdentityError(err)
	}
	return service.readOnboardingUpdateResponse(ctx, descriptor, response)
}

func (service OnboardingService) validateUpdate(command api.OnboardingUpdateCommand) error {
	if service.Pool == nil || service.Payloads == nil || service.StoreEpoch == "" || len(service.IdentityKey) < 32 || len(service.IdempotencyKeyPepper) < 32 || len(service.RequestDigestPepper) < 32 || service.IdempotencyTTL <= 0 {
		return api.ErrDependencyUnavailable
	}
	if command.RequestID == "" || command.ClientRequestID == "" || command.IdempotencyKey == "" || command.OnboardingSessionID == "" || command.TenantID == "" || command.UserID == "" || command.ExpectedOnboardingVersion < 1 || command.CurrentRole == nil && command.TargetRole == nil && command.ExperienceSummary == nil && command.WeeklyMinutes == nil {
		return api.ErrValidation
	}
	if command.CurrentRole != nil && (*command.CurrentRole == "" || utf8.RuneCountInString(*command.CurrentRole) > 500) || command.TargetRole != nil && (*command.TargetRole == "" || utf8.RuneCountInString(*command.TargetRole) > 500) || command.ExperienceSummary != nil && utf8.RuneCountInString(*command.ExperienceSummary) > 4000 || command.WeeklyMinutes != nil && (*command.WeeklyMinutes < 30 || *command.WeeklyMinutes > 2400) {
		return api.ErrValidation
	}
	if command.PrincipalKind == trustedcontext.AnonymousUser {
		if command.AnonymousSubjectID == "" || command.TenantID != service.Anonymous.SystemTenantID {
			return api.ErrPermissionDenied
		}
	} else if command.PrincipalKind == trustedcontext.AuthenticatedUser {
		if command.AnonymousSubjectID != "" {
			return api.ErrPermissionDenied
		}
	} else {
		return api.ErrPermissionDenied
	}
	return nil
}

func (service OnboardingService) loadOnboardingUpdateSnapshot(ctx context.Context, command api.OnboardingUpdateCommand) (onboardingUpdateSnapshot, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return onboardingUpdateSnapshot{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return onboardingUpdateSnapshot{}, api.ErrDependencyUnavailable
	}
	var version uint64
	var status string
	var anonymousSubjectID *string
	var currentRoleInput, targetRoleInput json.RawMessage
	var encodedManifest *string
	err = tx.QueryRow(ctx, `SELECT version,status,anonymous_subject_id::text,current_role_input,target_role_input,experience_payload_ref FROM identity.onboarding_sessions WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND expires_at>$4`, command.TenantID, command.UserID, command.OnboardingSessionID, service.updateNow()).Scan(&version, &status, &anonymousSubjectID, &currentRoleInput, &targetRoleInput, &encodedManifest)
	if errors.Is(err, pgx.ErrNoRows) {
		return onboardingUpdateSnapshot{}, api.ErrResourceNotFound
	}
	if err != nil {
		return onboardingUpdateSnapshot{}, api.ErrDependencyUnavailable
	}
	if version != command.ExpectedOnboardingVersion {
		return onboardingUpdateSnapshot{}, api.ErrVersionConflict
	}
	if status != "collecting" {
		return onboardingUpdateSnapshot{}, api.ErrStateConflict
	}
	if command.PrincipalKind == trustedcontext.AnonymousUser && (anonymousSubjectID == nil || *anonymousSubjectID != command.AnonymousSubjectID) || command.PrincipalKind == trustedcontext.AuthenticatedUser && anonymousSubjectID != nil {
		return onboardingUpdateSnapshot{}, api.ErrPermissionDenied
	}
	var current struct {
		Text string `json:"text"`
	}
	var target struct {
		Text          string `json:"text"`
		WeeklyMinutes int    `json:"weekly_minutes"`
	}
	if json.Unmarshal(currentRoleInput, &current) != nil || json.Unmarshal(targetRoleInput, &target) != nil || current.Text == "" || target.Text == "" || target.WeeklyMinutes < 30 || encodedManifest == nil {
		return onboardingUpdateSnapshot{}, api.ErrDependencyUnavailable
	}
	var manifest payload.Manifest
	if json.Unmarshal([]byte(*encodedManifest), &manifest) != nil || manifest.Ref == "" || manifest.Hash == "" {
		return onboardingUpdateSnapshot{}, api.ErrDependencyUnavailable
	}
	body, err := service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.OnboardingSessionID, Class: "onboarding-body", ContentType: "application/json"}, manifest)
	if err != nil {
		return onboardingUpdateSnapshot{}, api.ErrDependencyUnavailable
	}
	var stored onboardingUpdateSnapshot
	if json.Unmarshal(body, &stored) != nil {
		return onboardingUpdateSnapshot{}, api.ErrDependencyUnavailable
	}
	stored.CurrentRole, stored.TargetRole, stored.WeeklyMinutes = current.Text, target.Text, target.WeeklyMinutes
	if err = tx.Commit(ctx); err != nil {
		return onboardingUpdateSnapshot{}, api.ErrDependencyUnavailable
	}
	return stored, nil
}

func (service OnboardingService) prepareOnboardingUpdate(ctx context.Context, command api.OnboardingUpdateCommand, recordID string, responseDescriptor payload.Descriptor, snapshot onboardingUpdateSnapshot) (onboardingUpdatePrepared, error) {
	if command.CurrentRole != nil {
		snapshot.CurrentRole = *command.CurrentRole
	}
	if command.TargetRole != nil {
		snapshot.TargetRole = *command.TargetRole
	}
	if command.ExperienceSummary != nil {
		snapshot.ExperienceSummary = *command.ExperienceSummary
	}
	if command.WeeklyMinutes != nil {
		snapshot.WeeklyMinutes = *command.WeeklyMinutes
	}
	prepared := onboardingUpdatePrepared{}
	var err error
	prepared.EventID, err = ids.DeterministicUUID(service.IdentityKey, "onboarding-update:event", recordID)
	if err != nil {
		return prepared, api.ErrDependencyUnavailable
	}
	prepared.OutboxID, _ = ids.DeterministicUUID(service.IdentityKey, "onboarding-update:outbox", recordID)
	prepared.PublishID, _ = ids.DeterministicUUID(service.IdentityKey, "onboarding-update:publish", recordID)
	prepared.CurrentRoleInput, _ = json.Marshal(map[string]any{"text": snapshot.CurrentRole})
	prepared.TargetRoleInput, _ = json.Marshal(map[string]any{"text": snapshot.TargetRole, "weekly_minutes": snapshot.WeeklyMinutes})
	bodyManifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.OnboardingSessionID, Class: "onboarding-body", ContentType: "application/json"}, map[string]any{"current_role": snapshot.CurrentRole, "target_role": snapshot.TargetRole, "experience_summary": snapshot.ExperienceSummary, "weekly_minutes": snapshot.WeeklyMinutes})
	if err != nil {
		return prepared, api.ErrDependencyUnavailable
	}
	encodedManifest, _ := json.Marshal(bodyManifest)
	prepared.ExperienceManifest = string(encodedManifest)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": command.OnboardingSessionID, "subject_version": command.ExpectedOnboardingVersion + 1, "previous_state": "collecting", "new_state": "collecting", "reason_code": "intake_updated"})
	if err != nil {
		return prepared, api.ErrDependencyUnavailable
	}
	prepared.Result = api.OnboardingUpdateResult{ID: command.OnboardingSessionID, Version: command.ExpectedOnboardingVersion + 1, Status: "collecting", UpdatedAt: service.updateNow()}
	prepared.ResponsePayload, err = service.putJSON(ctx, responseDescriptor, prepared.Result)
	if err != nil {
		return prepared, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service OnboardingService) commitOnboardingUpdate(ctx context.Context, tx pgx.Tx, command api.OnboardingUpdateCommand, prepared onboardingUpdatePrepared) (idempotency.Response, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return idempotency.Response{}, err
	}
	var version uint64
	var status string
	var anonymousSubjectID *string
	err := tx.QueryRow(ctx, `SELECT version,status,anonymous_subject_id::text FROM identity.onboarding_sessions WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, command.TenantID, command.UserID, command.OnboardingSessionID).Scan(&version, &status, &anonymousSubjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Response{}, api.ErrResourceNotFound
	}
	if err != nil {
		return idempotency.Response{}, err
	}
	if version != command.ExpectedOnboardingVersion {
		return idempotency.Response{}, api.ErrVersionConflict
	}
	if status != "collecting" {
		return idempotency.Response{}, api.ErrStateConflict
	}
	if command.PrincipalKind == trustedcontext.AnonymousUser && (anonymousSubjectID == nil || *anonymousSubjectID != command.AnonymousSubjectID) || command.PrincipalKind == trustedcontext.AuthenticatedUser && anonymousSubjectID != nil {
		return idempotency.Response{}, api.ErrPermissionDenied
	}
	tag, err := tx.Exec(ctx, `UPDATE identity.onboarding_sessions SET version=version+1,current_role_input=$1,target_role_input=$2,experience_payload_ref=$3,updated_at=$4 WHERE tenant_id=$5 AND user_id=$6 AND id=$7 AND version=$8 AND status='collecting'`, prepared.CurrentRoleInput, prepared.TargetRoleInput, prepared.ExperienceManifest, prepared.Result.UpdatedAt, command.TenantID, command.UserID, command.OnboardingSessionID, command.ExpectedOnboardingVersion)
	if err != nil || tag.RowsAffected() != 1 {
		return idempotency.Response{}, api.ErrVersionConflict
	}
	actor, _ := json.Marshal(map[string]any{"kind": "user", "user_id": command.UserID})
	if command.PrincipalKind == trustedcontext.AnonymousUser {
		actor, _ = json.Marshal(map[string]any{"kind": "anonymous_user", "anonymous_subject_id": command.AnonymousSubjectID})
	}
	if _, err = service.Appender.Append(ctx, tx, onboardingUpdateEventInput(prepared, command, actor, service.StoreEpoch)); err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.ResponsePayload.Ref, Hash: prepared.ResponsePayload.Hash, ResourceVersion: prepared.Result.Version}, nil
}

func onboardingUpdateEventInput(prepared onboardingUpdatePrepared, command api.OnboardingUpdateCommand, actor json.RawMessage, storeEpoch string) eventpostgres.Input {
	return eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "OnboardingSessionUpdated", SchemaVersion: 1, AggregateKind: "onboarding_session", AggregateID: command.OnboardingSessionID, AggregateVersion: prepared.Result.Version, StoreEpoch: storeEpoch, OccurredAt: prepared.Result.UpdatedAt, Actor: actor, CorrelationID: command.RequestID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.OutboxID, CommandID: prepared.PublishID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}}}
}

func (service OnboardingService) readOnboardingUpdateResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.OnboardingUpdateResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return api.OnboardingUpdateResult{}, api.ErrDependencyUnavailable
	}
	var result api.OnboardingUpdateResult
	if json.Unmarshal(encoded, &result) != nil || result.ID == "" || result.Version < 2 || result.Status != "collecting" || result.UpdatedAt.IsZero() {
		return api.OnboardingUpdateResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

func (service OnboardingService) updateNow() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
