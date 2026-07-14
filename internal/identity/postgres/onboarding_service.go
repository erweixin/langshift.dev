package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

type OnboardingService struct {
	Pool                 *pgxpool.Pool
	Anonymous            AnonymousSessionService
	Payloads             payload.Store
	Appender             eventpostgres.Appender
	StoreEpoch           string
	IdentityKey          []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	IdempotencyTTL       time.Duration
	OnboardingTTL        time.Duration
	Random               io.Reader
	Now                  func() time.Time
}

type onboardingPrepared struct {
	Bootstrap                    *anonymousBootstrapPrepared
	SessionID                    string
	TenantID                     string
	UserID                       string
	AnonymousSubjectID           string
	EventID, OutboxID, CommandID string
	BodyPayload                  payload.Manifest
	EventPayload                 payload.Manifest
	ResponsePayload              payload.Manifest
	ExperienceManifest           string
	CurrentRoleInput             json.RawMessage
	TargetRoleInput              json.RawMessage
	ExpiresAt                    time.Time
}

func (service OnboardingService) CreateOnboarding(ctx context.Context, command api.OnboardingCreateCommand) (api.OnboardingCreateResult, error) {
	if err := service.validate(command); err != nil {
		return api.OnboardingCreateResult{}, err
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	scopeTenant, scopeUser, err := service.idempotencyPrincipal(command)
	if err != nil {
		return api.OnboardingCreateResult{}, api.ErrValidation
	}
	canonical, err := json.Marshal(struct {
		RequestID, PrincipalKind, UserID, TenantID, AnonymousSubjectID string
		CurrentRole, TargetRole, ExperienceSummary                     string
		WeeklyMinutes                                                  int
	}{command.ClientRequestID, string(command.PrincipalKind), command.UserID, command.TenantID, command.AnonymousSubjectID, command.CurrentRole, command.TargetRole, command.ExperienceSummary, command.WeeklyMinutes})
	if err != nil {
		return api.OnboardingCreateResult{}, api.ErrDependencyUnavailable
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.OnboardingCreateResult{}, api.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IdentityKey, "idempotency-record:onboarding.create", scopeTenant+"\x00"+scopeUser+"\x00"+command.IdempotencyKey)
	if err != nil {
		return api.OnboardingCreateResult{}, api.ErrDependencyUnavailable
	}
	idempotencyInput := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: scopeTenant, UserID: scopeUser, OperationID: "onboarding.create"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	descriptor := payload.Descriptor{TenantID: scopeTenant, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	executor := IdempotencyExecutor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, replayErr := executor.LoadCompleted(ctx, idempotencyInput); replayErr != nil {
		return api.OnboardingCreateResult{}, mapIdentityError(replayErr)
	} else if found {
		return service.readOnboardingResponse(ctx, descriptor, response)
	}
	prepared, err := service.prepare(ctx, command, descriptor, now)
	if err != nil {
		return api.OnboardingCreateResult{}, err
	}
	response, _, err := executor.Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		return service.commit(ctx, tx, command, prepared, now)
	})
	if err != nil {
		return api.OnboardingCreateResult{}, mapIdentityError(err)
	}
	return service.readOnboardingResponse(ctx, descriptor, response)
}

func (service OnboardingService) validate(command api.OnboardingCreateCommand) error {
	if service.Pool == nil || service.Payloads == nil || service.StoreEpoch == "" || service.Random == nil || len(service.IdentityKey) < 32 || len(service.IdempotencyKeyPepper) < 32 || len(service.RequestDigestPepper) < 32 || service.IdempotencyTTL <= 0 || service.OnboardingTTL <= 0 || service.OnboardingTTL > 30*24*time.Hour || service.Anonymous.SystemTenantID == "" || service.Anonymous.Payloads == nil || service.Anonymous.StoreEpoch != service.StoreEpoch {
		return api.ErrDependencyUnavailable
	}
	if command.CurrentRole == "" || command.TargetRole == "" || utf8.RuneCountInString(command.CurrentRole) > 500 || utf8.RuneCountInString(command.TargetRole) > 500 || utf8.RuneCountInString(command.ExperienceSummary) > 4000 || command.WeeklyMinutes < 30 || command.WeeklyMinutes > 2400 {
		return api.ErrValidation
	}
	switch command.PrincipalKind {
	case trustedcontext.PublicRequest:
		if command.UserID != "" || command.TenantID != "" || command.AnonymousSubjectID != "" {
			return api.ErrValidation
		}
	case trustedcontext.AnonymousUser:
		if command.UserID == "" || command.TenantID == "" || command.AnonymousSubjectID == "" || command.TenantID != service.Anonymous.SystemTenantID {
			return api.ErrPermissionDenied
		}
	case trustedcontext.AuthenticatedUser:
		if command.UserID == "" || command.TenantID == "" || command.AnonymousSubjectID != "" {
			return api.ErrPermissionDenied
		}
	default:
		return api.ErrPermissionDenied
	}
	return nil
}

func (service OnboardingService) idempotencyPrincipal(command api.OnboardingCreateCommand) (string, string, error) {
	if command.PrincipalKind != trustedcontext.PublicRequest {
		return command.TenantID, command.UserID, nil
	}
	value := hex.EncodeToString(command.ClientIPHash) + "\x00" + hex.EncodeToString(command.UserAgentHash)
	userID, err := ids.DeterministicUUID(service.IdentityKey, "anonymous-onboarding-public-principal", value)
	return service.Anonymous.SystemTenantID, userID, err
}

func (service OnboardingService) prepare(ctx context.Context, command api.OnboardingCreateCommand, responseDescriptor payload.Descriptor, now time.Time) (onboardingPrepared, error) {
	prepared := onboardingPrepared{TenantID: command.TenantID, UserID: command.UserID, AnonymousSubjectID: command.AnonymousSubjectID, ExpiresAt: now.Add(service.OnboardingTTL)}
	if command.PrincipalKind == trustedcontext.PublicRequest {
		bootstrap, err := service.Anonymous.prepare(ctx, now)
		if err != nil {
			return onboardingPrepared{}, api.ErrDependencyUnavailable
		}
		prepared.Bootstrap = &bootstrap
		prepared.TenantID = bootstrap.Bootstrap.Principal.TenantID
		prepared.UserID = bootstrap.Bootstrap.Principal.UserID
		prepared.AnonymousSubjectID = bootstrap.Bootstrap.Principal.AnonymousSubjectID
		if bootstrap.Bootstrap.Principal.ExpiresAt.Before(prepared.ExpiresAt) {
			prepared.ExpiresAt = bootstrap.Bootstrap.Principal.ExpiresAt
		}
	}
	values := make([]string, 4)
	var err error
	for index := range values {
		values[index], err = ids.NewUUIDFrom(service.Random)
		if err != nil {
			return onboardingPrepared{}, api.ErrDependencyUnavailable
		}
	}
	prepared.SessionID, prepared.EventID, prepared.OutboxID, prepared.CommandID = values[0], values[1], values[2], values[3]
	prepared.CurrentRoleInput, _ = json.Marshal(map[string]any{"text": command.CurrentRole})
	prepared.TargetRoleInput, _ = json.Marshal(map[string]any{"text": command.TargetRole, "weekly_minutes": command.WeeklyMinutes})
	body := map[string]any{"current_role": command.CurrentRole, "target_role": command.TargetRole, "experience_summary": command.ExperienceSummary, "weekly_minutes": command.WeeklyMinutes}
	prepared.BodyPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: prepared.TenantID, ObjectID: prepared.SessionID, Class: "onboarding-body", ContentType: "application/json"}, body)
	if err != nil {
		return onboardingPrepared{}, api.ErrDependencyUnavailable
	}
	manifestJSON, err := json.Marshal(prepared.BodyPayload)
	if err != nil {
		return onboardingPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.ExperienceManifest = string(manifestJSON)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: prepared.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.SessionID, "subject_version": 1, "previous_state": nil, "new_state": "collecting", "reason_code": "created"})
	if err != nil {
		return onboardingPrepared{}, api.ErrDependencyUnavailable
	}
	handle, handleExpiry := "", time.Time{}
	if prepared.Bootstrap != nil {
		handle = prepared.Bootstrap.Bootstrap.Credential.Raw
		handleExpiry = prepared.Bootstrap.Bootstrap.Credential.ExpiresAt
	}
	prepared.ResponsePayload, err = service.putJSON(ctx, responseDescriptor, map[string]any{"id": prepared.SessionID, "version": 1, "status": "collecting", "updated_at": now, "anonymous_handle": handle, "handle_expires_at": handleExpiry})
	if err != nil {
		return onboardingPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service OnboardingService) commit(ctx context.Context, tx pgx.Tx, command api.OnboardingCreateCommand, prepared onboardingPrepared, now time.Time) (idempotency.Response, error) {
	if prepared.Bootstrap != nil {
		if err := service.Anonymous.commitPrepared(ctx, tx, *prepared.Bootstrap, now); err != nil {
			return idempotency.Response{}, err
		}
	} else if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, prepared.TenantID); err != nil {
		return idempotency.Response{}, err
	}
	if command.PrincipalKind == trustedcontext.AnonymousUser {
		var valid bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.anonymous_subjects WHERE id=$1 AND ephemeral_user_id=$2 AND system_tenant_id=$3 AND deleted_at IS NULL AND expires_at>$4)`, prepared.AnonymousSubjectID, prepared.UserID, prepared.TenantID, now).Scan(&valid)
		if err != nil || !valid {
			return idempotency.Response{}, api.ErrPermissionDenied
		}
	}
	if command.PrincipalKind == trustedcontext.AuthenticatedUser {
		var valid bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.users u JOIN identity.memberships m ON m.user_id=u.id AND m.tenant_id=$2 WHERE u.id=$1 AND u.status='active' AND u.email_verified_at IS NOT NULL AND m.status='active')`, prepared.UserID, prepared.TenantID).Scan(&valid)
		if err != nil || !valid {
			return idempotency.Response{}, api.ErrPermissionDenied
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.onboarding_sessions(id,tenant_id,user_id,anonymous_subject_id,status,locale,current_role_input,target_role_input,experience_payload_ref,confirmed_claim_ids,expires_at) VALUES($1,$2,$3,NULLIF($4,'')::uuid,'collecting','en',$5,$6,$7,'[]',$8)`, prepared.SessionID, prepared.TenantID, prepared.UserID, prepared.AnonymousSubjectID, prepared.CurrentRoleInput, prepared.TargetRoleInput, prepared.ExperienceManifest, prepared.ExpiresAt); err != nil {
		return idempotency.Response{}, err
	}
	actor := json.RawMessage(`{"kind":"system","id":"identity-service"}`)
	if command.PrincipalKind == trustedcontext.AuthenticatedUser {
		actor, _ = json.Marshal(map[string]any{"kind": "user", "id": prepared.UserID})
	}
	correlationID := prepared.EventID
	if prepared.Bootstrap != nil {
		correlationID = prepared.Bootstrap.EventID
	}
	if _, err := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: prepared.TenantID, UserID: prepared.UserID, EventType: "OnboardingSessionUpdated", SchemaVersion: 1, AggregateKind: "onboarding_session", AggregateID: prepared.SessionID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: correlationID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.OutboxID, CommandID: prepared.CommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}}}); err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.ResponsePayload.Ref, Hash: prepared.ResponsePayload.Hash, ResourceVersion: 1}, nil
}

func (service OnboardingService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service OnboardingService) readOnboardingResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.OnboardingCreateResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return api.OnboardingCreateResult{}, api.ErrDependencyUnavailable
	}
	var result api.OnboardingCreateResult
	if err = json.Unmarshal(encoded, &result); err != nil || result.ID == "" || result.Version < 1 || result.Status == "" || result.UpdatedAt.IsZero() {
		return api.OnboardingCreateResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

var _ api.OnboardingService = OnboardingService{}
