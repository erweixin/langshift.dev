package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
)

type tenantSwitchPrepared struct {
	EventID, OutboxID, CommandID, SecurityEventID string
	EventPayload, Response                        payload.Manifest
}

func (service AuthService) SwitchTenant(ctx context.Context, command api.SwitchTenantCommand) (api.SessionMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.SessionMutationResult{}, err
	}
	if uuid.Validate(command.ActiveTenantID) != nil || command.ExpectedSessionVersion == 0 {
		return api.SessionMutationResult{}, api.ErrValidation
	}
	if command.ActiveTenantID == command.TenantID {
		return api.SessionMutationResult{}, api.ErrStateConflict
	}
	recordID, requestHash, err := service.authenticatedIdempotency("auth.session.switch_tenant", command.UserID, command.IdempotencyKey, struct {
		ActiveTenantID         string `json:"active_tenant_id"`
		ExpectedSessionVersion uint64 `json:"expected_session_version"`
	}{command.ActiveTenantID, command.ExpectedSessionVersion})
	if err != nil {
		return api.SessionMutationResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: command.ActiveTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.ActiveTenantID, UserID: command.UserID, OperationID: "auth.session.switch_tenant"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadSessionMutationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeTenantSwitch(ctx, command); err != nil {
		return service.sessionMutationReplayOr(ctx, input, descriptor, err)
	}
	current, err := service.lookupOwnedSession(ctx, command.UserID, command.SessionID)
	if err != nil {
		return service.sessionMutationReplayOr(ctx, input, descriptor, err)
	}
	if current.ActiveTenantID != command.TenantID || current.Version != command.ExpectedSessionVersion || current.RevokedAt != nil {
		return service.sessionMutationReplayOr(ctx, input, descriptor, api.ErrVersionConflict)
	}
	now := service.now()
	prepared, err := service.prepareTenantSwitch(ctx, command, current.ActiveTenantID, recordID, now)
	if err != nil {
		return api.SessionMutationResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if err := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); err != nil {
			return idempotency.Response{}, err
		}
		var activeTenantID string
		var version uint64
		if err := tx.QueryRow(ctx, `SELECT active_tenant_id::text,version FROM identity.sessions WHERE id=$1 AND user_id=$2`, command.SessionID, command.UserID).Scan(&activeTenantID, &version); err != nil {
			return idempotency.Response{}, err
		}
		if activeTenantID != command.TenantID || version != command.ExpectedSessionVersion {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.ActiveTenantID); err != nil {
			return idempotency.Response{}, err
		}
		var membershipID string
		err := tx.QueryRow(ctx, `SELECT m.id::text FROM identity.memberships m JOIN identity.tenants t ON t.id=m.tenant_id WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND t.status='active'`, command.ActiveTenantID, command.UserID).Scan(&membershipID)
		if errors.Is(err, pgx.ErrNoRows) {
			return idempotency.Response{}, api.ErrPermissionDenied
		}
		if err != nil {
			return idempotency.Response{}, err
		}
		tag, err := tx.Exec(ctx, `UPDATE identity.sessions SET active_tenant_id=$1,version=version+1,updated_at=$2 WHERE id=$3 AND user_id=$4 AND active_tenant_id=$5 AND version=$6 AND revoked_at IS NULL`, command.ActiveTenantID, now, command.SessionID, command.UserID, command.TenantID, command.ExpectedSessionVersion)
		if err != nil {
			return idempotency.Response{}, err
		}
		if tag.RowsAffected() != 1 {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		details, _ := json.Marshal(map[string]string{"previous_tenant_id": command.TenantID, "active_tenant_id": command.ActiveTenantID, "membership_id": membershipID})
		if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'session_active_tenant_changed',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, command.ActiveTenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, now); err != nil {
			return idempotency.Response{}, err
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		if _, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.ActiveTenantID, UserID: command.UserID, EventType: "SessionActiveTenantChanged", SchemaVersion: 1, AggregateKind: "session", AggregateID: command.SessionID, AggregateVersion: command.ExpectedSessionVersion + 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.OutboxID, CommandID: prepared.CommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}}}); err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: command.ExpectedSessionVersion + 1}, nil
	})
	if err != nil {
		return api.SessionMutationResult{}, mapIdentityError(err)
	}
	var result api.SessionMutationResult
	if err = service.readResponse(ctx, descriptor, response, &result); err != nil {
		return api.SessionMutationResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

func (service AuthService) preauthorizeTenantSwitch(ctx context.Context, command api.SwitchTenantCommand) error {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, service.now(), false); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.ActiveTenantID); err != nil {
		return api.ErrDependencyUnavailable
	}
	var membershipID string
	err = tx.QueryRow(ctx, `SELECT m.id::text FROM identity.memberships m JOIN identity.tenants t ON t.id=m.tenant_id WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND t.status='active'`, command.ActiveTenantID, command.UserID).Scan(&membershipID)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.ErrPermissionDenied
	}
	if err != nil {
		return api.ErrDependencyUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return api.ErrDependencyUnavailable
	}
	return nil
}

func (service AuthService) prepareTenantSwitch(ctx context.Context, command api.SwitchTenantCommand, previousTenantID, recordID string, now time.Time) (tenantSwitchPrepared, error) {
	prepared := tenantSwitchPrepared{}
	for _, target := range []*string{&prepared.EventID, &prepared.OutboxID, &prepared.CommandID, &prepared.SecurityEventID} {
		value, err := service.newID()
		if err != nil {
			return tenantSwitchPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	nextVersion := command.ExpectedSessionVersion + 1
	var err error
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.ActiveTenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": command.SessionID, "subject_version": nextVersion, "previous_state": previousTenantID, "new_state": command.ActiveTenantID, "reason_code": "user_selected_tenant"})
	if err != nil {
		return tenantSwitchPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.ActiveTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, api.SessionMutationResult{ID: command.SessionID, Version: nextVersion, Status: "active_tenant_changed", UpdatedAt: now})
	if err != nil {
		return tenantSwitchPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}
