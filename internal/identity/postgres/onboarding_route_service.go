package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const onboardingEmptyClaimSetHash = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"

type OnboardingRouteService struct {
	Pool                 *pgxpool.Pool
	Payloads             payload.Store
	Appender             eventpostgres.Appender
	StoreEpoch           string
	IdentityKey          []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	IdempotencyTTL       time.Duration
	Now                  func() time.Time
}

type onboardingRoutePrepared struct {
	MissionID, EventID, PublishOutboxID, PublishCommandID string
	RouteOutboxID, RouteCommandID                         string
	Goal, Event, Command, Response                        payload.Manifest
}

func (service OnboardingRouteService) RequestRoute(ctx context.Context, command api.OnboardingRouteCommand) (api.OnboardingRouteRequestResult, error) {
	if !service.valid() || command.RequestID == "" || command.ClientRequestID == "" || command.IdempotencyKey == "" || command.OnboardingSessionID == "" || command.TenantID == "" || command.UserID == "" || command.TargetRoleProfileID == "" || command.ExpectedOnboardingVersion < 1 || command.ConfirmedClaimIDs == nil {
		return api.OnboardingRouteRequestResult{}, api.ErrValidation
	}
	canonical, err := json.Marshal(command)
	if err != nil {
		return api.OnboardingRouteRequestResult{}, api.ErrDependencyUnavailable
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.OnboardingRouteRequestResult{}, api.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IdentityKey, "idempotency-record:onboarding.route-preview", command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return api.OnboardingRouteRequestResult{}, api.ErrDependencyUnavailable
	}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "onboarding.route-preview"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	executor := IdempotencyExecutor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return api.OnboardingRouteRequestResult{}, mapIdentityError(loadErr)
	} else if found {
		result, readErr := service.readResponse(ctx, descriptor, response)
		result.Version = response.ResourceVersion
		result.Replayed = readErr == nil
		return result, readErr
	}
	prepared, err := service.prepare(ctx, command, descriptor)
	if err != nil {
		return api.OnboardingRouteRequestResult{}, err
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		return service.commit(ctx, tx, command, prepared)
	})
	if err != nil {
		return api.OnboardingRouteRequestResult{}, mapIdentityError(err)
	}
	result, err := service.readResponse(ctx, descriptor, response)
	result.Version = response.ResourceVersion
	result.Replayed = replayed
	return result, err
}

func (service OnboardingRouteService) prepare(ctx context.Context, command api.OnboardingRouteCommand, descriptor payload.Descriptor) (onboardingRoutePrepared, error) {
	prepared := onboardingRoutePrepared{}
	var err error
	values := []*string{&prepared.MissionID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID, &prepared.RouteOutboxID, &prepared.RouteCommandID}
	for index, target := range values {
		*target, err = ids.DeterministicUUID(service.IdentityKey, "onboarding-route-preview:"+[]string{"mission", "event", "publish-outbox", "publish-command", "route-outbox", "route-command"}[index], command.TenantID+"\x00"+command.OnboardingSessionID)
		if err != nil {
			return onboardingRoutePrepared{}, api.ErrDependencyUnavailable
		}
	}
	put := func(objectID, class string, value any) (payload.Manifest, error) {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return payload.Manifest{}, marshalErr
		}
		return service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
	}
	prepared.Goal, err = put(prepared.MissionID, "mission-goal", map[string]any{"goal": "Build an evidence-backed transition route from the onboarding intake", "onboarding_session_id": command.OnboardingSessionID})
	if err != nil {
		return onboardingRoutePrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Event, err = put(prepared.EventID, "event-payload", map[string]any{"subject_id": command.OnboardingSessionID, "subject_version": command.ExpectedOnboardingVersion + 1, "request_id": command.ClientRequestID, "mission_id": prepared.MissionID, "previous_state": "collecting", "new_state": "route_generating", "reason_code": "route_preview_requested"})
	if err != nil {
		return onboardingRoutePrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Command, err = put(prepared.RouteCommandID, "product-command", map[string]any{"schema_version": 1, "mission_id": prepared.MissionID, "user_id": command.UserID, "expected_route_version": 0, "expected_claim_set_hash": onboardingEmptyClaimSetHash, "correlation_id": command.RequestID, "onboarding_session_id": command.OnboardingSessionID, "anonymous_subject_id": command.AnonymousSubjectID})
	if err != nil {
		return onboardingRoutePrepared{}, api.ErrDependencyUnavailable
	}
	result := api.OnboardingRouteRequestResult{RunID: prepared.MissionID, Version: command.ExpectedOnboardingVersion + 1, Status: "accepted", AcceptedAt: service.now()}
	prepared.Response, err = put(descriptor.ObjectID, descriptor.Class, result)
	if err != nil {
		return onboardingRoutePrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service OnboardingRouteService) commit(ctx context.Context, tx pgx.Tx, command api.OnboardingRouteCommand, prepared onboardingRoutePrepared) (idempotency.Response, error) {
	now := service.now()
	var version uint64
	var status string
	var anonymousSubjectID *string
	err := tx.QueryRow(ctx, `SELECT version,status,anonymous_subject_id::text FROM identity.onboarding_sessions WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND expires_at>$4 FOR UPDATE`, command.TenantID, command.UserID, command.OnboardingSessionID, now).Scan(&version, &status, &anonymousSubjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Response{}, api.ErrResourceNotFound
	}
	if err != nil {
		return idempotency.Response{}, err
	}
	if version != command.ExpectedOnboardingVersion {
		return idempotency.Response{}, api.ErrVersionConflict
	}
	if status != "collecting" && status != "route_failed" {
		return idempotency.Response{}, api.ErrStateConflict
	}
	if command.AnonymousSubjectID != "" && (anonymousSubjectID == nil || *anonymousSubjectID != command.AnonymousSubjectID) || command.AnonymousSubjectID == "" && anonymousSubjectID != nil {
		return idempotency.Response{}, api.ErrPermissionDenied
	}
	var targetValid, sourceValid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.role_profiles WHERE tenant_id=$1 AND id=$2 AND status='active'),$3::uuid IS NULL OR EXISTS(SELECT 1 FROM product.role_profiles WHERE tenant_id=$1 AND id=$3 AND status='active')`, command.TenantID, command.TargetRoleProfileID, nullableString(command.SourceRoleProfileID)).Scan(&targetValid, &sourceValid); err != nil {
		return idempotency.Response{}, err
	}
	if !targetValid || !sourceValid {
		return idempotency.Response{}, api.ErrResourceNotFound
	}
	if _, err = tx.Exec(ctx, `INSERT INTO product.missions(id,tenant_id,user_id,status,source_role_profile_id,target_role_profile_id,goal_payload_ref,goal_payload_hash,route_version,claim_set_hash,created_at,updated_at) VALUES($1,$2,$3,'draft',NULLIF($4,'')::uuid,$5,$6,$7,0,$8,$9,$9)`, prepared.MissionID, command.TenantID, command.UserID, command.SourceRoleProfileID, command.TargetRoleProfileID, prepared.Goal.Ref, prepared.Goal.Hash, onboardingEmptyClaimSetHash, now); err != nil {
		return idempotency.Response{}, err
	}
	claimsJSON, _ := json.Marshal(command.ConfirmedClaimIDs)
	tag, err := tx.Exec(ctx, `UPDATE identity.onboarding_sessions SET version=version+1,status='route_generating',mission_id=$1,route_revision_id=NULL,confirmed_claim_ids=$2,updated_at=$3 WHERE tenant_id=$4 AND user_id=$5 AND id=$6 AND version=$7 AND status=$8`, prepared.MissionID, claimsJSON, now, command.TenantID, command.UserID, command.OnboardingSessionID, version, status)
	if err != nil || tag.RowsAffected() != 1 {
		return idempotency.Response{}, api.ErrVersionConflict
	}
	actor, _ := json.Marshal(map[string]any{"kind": "user", "id": command.UserID})
	if command.AnonymousSubjectID != "" {
		actor, _ = json.Marshal(map[string]any{"kind": "anonymous", "id": command.AnonymousSubjectID})
	}
	_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "RoutePreviewRequested", SchemaVersion: 1, AggregateKind: "onboarding_session", AggregateID: command.OnboardingSessionID, AggregateVersion: version + 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: command.RequestID, PayloadRef: prepared.Event.Ref, PayloadHash: prepared.Event.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.Event.Ref, PayloadHash: prepared.Event.Hash}, {ID: prepared.RouteOutboxID, CommandID: prepared.RouteCommandID, CommandType: "GenerateMissionRoute", TargetAggregateKind: "mission", TargetAggregateID: prepared.MissionID, PayloadRef: prepared.Command.Ref, PayloadHash: prepared.Command.Hash}}})
	if err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: http.StatusAccepted, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: version + 1}, nil
}

func (service OnboardingRouteService) GetRoute(ctx context.Context, sessionID, tenantID, userID, anonymousSubjectID string) (api.OnboardingRouteResource, error) {
	if !service.valid() || sessionID == "" || tenantID == "" || userID == "" {
		return api.OnboardingRouteResource{}, api.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return api.OnboardingRouteResource{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return api.OnboardingRouteResource{}, api.ErrDependencyUnavailable
	}
	var result api.OnboardingRouteResource
	var storedAnonymous *string
	var routeRef, routeHash *string
	err = tx.QueryRow(ctx, `SELECT s.id::text,s.version,s.status,s.mission_id::text,s.route_revision_id::text,s.anonymous_subject_id::text,s.updated_at,r.route_payload_ref,r.route_payload_hash,(SELECT c.version FROM identity.onboarding_claims c WHERE c.tenant_id=s.tenant_id AND c.onboarding_session_id=s.id ORDER BY c.created_at DESC LIMIT 1) FROM identity.onboarding_sessions s LEFT JOIN product.route_revisions r ON r.tenant_id=s.tenant_id AND r.id=s.route_revision_id WHERE s.tenant_id=$1 AND s.user_id=$2 AND s.id=$3 AND s.expires_at>$4`, tenantID, userID, sessionID, service.now()).Scan(&result.ID, &result.Version, &result.Status, &result.MissionID, &result.RouteRevisionID, &storedAnonymous, &result.UpdatedAt, &routeRef, &routeHash, &result.ClaimVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.OnboardingRouteResource{}, api.ErrResourceNotFound
	}
	if err != nil {
		return api.OnboardingRouteResource{}, api.ErrDependencyUnavailable
	}
	if anonymousSubjectID != "" && (storedAnonymous == nil || *storedAnonymous != anonymousSubjectID) || anonymousSubjectID == "" && storedAnonymous != nil {
		return api.OnboardingRouteResource{}, api.ErrPermissionDenied
	}
	if err = tx.Commit(ctx); err != nil {
		return api.OnboardingRouteResource{}, api.ErrDependencyUnavailable
	}
	result.Route = json.RawMessage("null")
	if result.Status == "route_ready" && result.RouteRevisionID != nil && routeRef != nil && routeHash != nil {
		encoded, getErr := service.Payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: *result.RouteRevisionID, Class: "route-revision", ContentType: "application/json"}, payload.Manifest{Ref: *routeRef, Hash: *routeHash})
		if getErr != nil || !json.Valid(encoded) {
			return api.OnboardingRouteResource{}, api.ErrDependencyUnavailable
		}
		result.Route = encoded
	}
	return result, nil
}

func (service OnboardingRouteService) readResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.OnboardingRouteRequestResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return api.OnboardingRouteRequestResult{}, api.ErrDependencyUnavailable
	}
	var result api.OnboardingRouteRequestResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || result.RunID == "" || result.Status != "accepted" || result.AcceptedAt.IsZero() {
		return api.OnboardingRouteRequestResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

func (service OnboardingRouteService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && service.StoreEpoch != "" && len(service.IdentityKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0
}
func (service OnboardingRouteService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

var _ api.OnboardingRouteService = OnboardingRouteService{}
