package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/payload"
)

type reauthenticationLookup struct {
	SessionVersion    uint64
	CredentialVersion uint64
	PasswordHash      []byte
	Parameters        password.Parameters
}

func (service AuthService) Reauthenticate(ctx context.Context, command api.ReauthenticateCommand) (api.ReauthenticationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.ReauthenticationResult{}, err
	}
	if password.ValidateForAuthentication(command.Password) != nil {
		return api.ReauthenticationResult{}, api.ErrValidation
	}
	if err := service.preauthorizeSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		return api.ReauthenticationResult{}, err
	}
	recordID, err := service.idempotencyRecordID("auth.reauthentication", command.UserID, command.IdempotencyKey)
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrIdempotencyConflict
	}
	canonical, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Password  string `json:"password"`
	}{SessionID: command.SessionID, Password: command.Password})
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	idempotencyInput := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "auth.reauthentication"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if response, found, replayErr := service.idempotencyExecutor().LoadCompleted(ctx, idempotencyInput); replayErr != nil {
		return api.ReauthenticationResult{}, mapIdentityError(replayErr)
	} else if found {
		result, readErr := service.readReauthenticationResponse(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, readErr
	}

	lookup, err := service.lookupReauthentication(ctx, command.UserID, command.SessionID)
	if err != nil {
		return api.ReauthenticationResult{}, err
	}
	verified, verifyErr := service.Passwords.Verify(command.Password, lookup.PasswordHash, lookup.Parameters)
	if verifyErr != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	if !verified {
		if auditErr := service.recordReauthenticationFailure(ctx, command.AuthenticatedRequestMetadata); auditErr != nil {
			return api.ReauthenticationResult{}, auditErr
		}
		return api.ReauthenticationResult{}, api.ErrInvalidCredentials
	}

	now := service.now()
	result := api.ReauthenticationResult{SessionID: command.SessionID, SessionVersion: lookup.SessionVersion + 1, ReauthenticatedAt: now, ValidUntil: now.Add(service.ReauthenticationTTL)}
	responseManifest, err := service.putJSON(ctx, descriptor, result)
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	securityEventID, err := service.newID()
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	eventID, err := service.newID()
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	outboxID, err := service.newID()
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	commandID, err := service.newID()
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"session_id": command.SessionID, "session_version": result.SessionVersion, "reauthenticated_at": now, "valid_until": result.ValidUntil, "method": "password"})
	if err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}

	response, replayed, err := service.idempotencyExecutor().Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if err := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); err != nil {
			return idempotency.Response{}, err
		}
		var sessionVersion, credentialVersion uint64
		if err := tx.QueryRow(ctx, `SELECT version FROM identity.sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, command.SessionID, command.UserID).Scan(&sessionVersion); err != nil {
			return idempotency.Response{}, err
		}
		if err := tx.QueryRow(ctx, `SELECT version FROM identity.password_credentials WHERE user_id=$1 FOR SHARE`, command.UserID).Scan(&credentialVersion); err != nil {
			return idempotency.Response{}, err
		}
		if sessionVersion != lookup.SessionVersion || credentialVersion != lookup.CredentialVersion {
			return idempotency.Response{}, api.ErrStateConflict
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE identity.sessions SET version=version+1,reauthenticated_at=$1,last_seen_at=GREATEST(last_seen_at,$1),updated_at=$1 WHERE id=$2 AND user_id=$3 AND version=$4 AND revoked_at IS NULL AND expires_at>$1`, now, command.SessionID, command.UserID, lookup.SessionVersion)
		if updateErr != nil {
			return idempotency.Response{}, updateErr
		}
		if tag.RowsAffected() != 1 {
			return idempotency.Response{}, api.ErrStateConflict
		}
		details, _ := json.Marshal(map[string]any{"session_version": result.SessionVersion, "method": "password", "valid_until": result.ValidUntil})
		if _, insertErr := tx.Exec(ctx, `INSERT INTO identity.security_events(id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES($1,$2,$3,$3,'session_reauthenticated',$4,$5,$6,$7,$8)`, securityEventID, command.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, now); insertErr != nil {
			return idempotency.Response{}, insertErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		if _, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "SessionReauthenticated", SchemaVersion: 1, AggregateKind: "session", AggregateID: command.SessionID, AggregateVersion: result.SessionVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: securityEventID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: commandID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}}); appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: result.SessionVersion}, nil
	})
	if err != nil {
		return api.ReauthenticationResult{}, mapIdentityError(err)
	}
	stored, err := service.readReauthenticationResponse(ctx, descriptor, response)
	if err != nil {
		return api.ReauthenticationResult{}, err
	}
	stored.Replayed = replayed
	return stored, nil
}

func (service AuthService) lookupReauthentication(ctx context.Context, userID, sessionID string) (reauthenticationLookup, error) {
	var lookup reauthenticationLookup
	var parameters []byte
	err := service.Pool.QueryRow(ctx, `SELECT s.version,pc.version,pc.password_hash,pc.parameters FROM identity.sessions s JOIN identity.users u ON u.id=s.user_id JOIN identity.password_credentials pc ON pc.user_id=u.id WHERE s.id=$1 AND s.user_id=$2 AND s.revoked_at IS NULL AND u.status='active' AND u.email_verified_at IS NOT NULL`, sessionID, userID).Scan(&lookup.SessionVersion, &lookup.CredentialVersion, &lookup.PasswordHash, &parameters)
	if errors.Is(err, pgx.ErrNoRows) {
		return reauthenticationLookup{}, api.ErrInvalidCredentials
	}
	if err != nil || json.Unmarshal(parameters, &lookup.Parameters) != nil {
		return reauthenticationLookup{}, api.ErrDependencyUnavailable
	}
	return lookup, nil
}

func (service AuthService) recordReauthenticationFailure(ctx context.Context, metadata api.AuthenticatedRequestMetadata) error {
	id, err := service.newID()
	if err != nil {
		return api.ErrDependencyUnavailable
	}
	tx, err := service.Pool.Begin(ctx)
	if err != nil {
		return api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, metadata.TenantID); err != nil {
		return api.ErrDependencyUnavailable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events(id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES($1,$2,$3,$3,'session_reauthentication_failed',$4,$5,$6,'{"method":"password"}',$7)`, id, metadata.TenantID, metadata.UserID, metadata.RequestID, metadata.ClientIPHash, metadata.UserAgentHash, service.now()); err != nil {
		return api.ErrDependencyUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return api.ErrDependencyUnavailable
	}
	return nil
}

func (service AuthService) readReauthenticationResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.ReauthenticationResult, error) {
	var result api.ReauthenticationResult
	if err := service.readResponse(ctx, descriptor, response, &result); err != nil {
		return api.ReauthenticationResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

var _ api.SessionService = AuthService{}
