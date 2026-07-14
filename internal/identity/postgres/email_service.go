package postgres

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/payload"
)

type emailChangeRequestPrepared struct {
	VerificationID, SecurityEventID, EventID, PublishOutboxID, PublishCommandID          string
	VerifyMailOutboxID, VerifyMailCommandID, SecurityMailOutboxID, SecurityMailCommandID string
	TokenDigest                                                                          []byte
	ExpiresAt                                                                            time.Time
	EventPayload, VerifyMailCommand, SecurityMailCommand, Response                       payload.Manifest
}

type storedEmailChangeResponse struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (service AuthService) ChangeEmail(ctx context.Context, command api.EmailChangeCommand) (api.EmailChangeResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.EmailChangeResult{}, err
	}
	if command.NewNormalizedEmail == "" || command.CurrentPassword == "" || command.ExpectedUserVersion == 0 {
		return api.EmailChangeResult{}, api.ErrValidation
	}
	recordID, err := service.idempotencyRecordID("auth.email_change", command.UserID, command.IdempotencyKey)
	if err != nil {
		return api.EmailChangeResult{}, api.ErrIdempotencyConflict
	}
	canonical, _ := json.Marshal(struct {
		NewEmail        string `json:"new_email"`
		CurrentPassword string `json:"current_password"`
		ExpectedVersion uint64 `json:"expected_user_version"`
	}{command.NewNormalizedEmail, command.CurrentPassword, command.ExpectedUserVersion})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.EmailChangeResult{}, api.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "auth.email_change"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadEmailChangeReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		return service.emailChangeReplayOr(ctx, input, descriptor, err)
	}
	lookup, err := service.lookupPasswordChange(ctx, command.UserID, command.SessionID)
	if err != nil {
		return service.emailChangeReplayOr(ctx, input, descriptor, mapLookupError(err))
	}
	if lookup.Session.ActiveTenantID != command.TenantID || lookup.Status != "active" {
		return service.emailChangeReplayOr(ctx, input, descriptor, api.ErrInvalidCredentials)
	}
	if lookup.UserVersion != command.ExpectedUserVersion {
		return service.emailChangeReplayOr(ctx, input, descriptor, api.ErrVersionConflict)
	}
	if lookup.Email == command.NewNormalizedEmail {
		return service.emailChangeReplayOr(ctx, input, descriptor, api.ErrValidation)
	}
	verified, verifyErr := service.Passwords.Verify(command.CurrentPassword, lookup.PasswordHash, lookup.PasswordParameters)
	if verifyErr != nil {
		return api.EmailChangeResult{}, api.ErrDependencyUnavailable
	}
	if !verified {
		return service.emailChangeReplayOr(ctx, input, descriptor, api.ErrReauthenticationRequired)
	}
	now := service.now()
	prepared, err := service.prepareEmailChangeRequest(ctx, lookup, command, recordID, now)
	if err != nil {
		return api.EmailChangeResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		var currentEmail, status string
		var userVersion uint64
		if err := tx.QueryRow(ctx, `SELECT normalized_email,status,version FROM identity.users WHERE id=$1 FOR UPDATE`, command.UserID).Scan(&currentEmail, &status, &userVersion); err != nil {
			return idempotency.Response{}, err
		}
		if err := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); err != nil {
			return idempotency.Response{}, err
		}
		currentSession, err := service.lockOwnedSession(ctx, tx, command.UserID, command.SessionID)
		if err != nil {
			return idempotency.Response{}, err
		}
		var credentialVersion uint64
		var storedHash []byte
		if err = tx.QueryRow(ctx, `SELECT version,password_hash FROM identity.password_credentials WHERE id=$1 AND user_id=$2 FOR UPDATE`, lookup.CredentialID, command.UserID).Scan(&credentialVersion, &storedHash); err != nil {
			return idempotency.Response{}, err
		}
		if status != "active" || currentEmail != lookup.Email || userVersion != lookup.UserVersion || userVersion != command.ExpectedUserVersion || currentSession.Version != lookup.Session.Version || credentialVersion != lookup.CredentialVersion || !hmac.Equal(storedHash, lookup.PasswordHash) {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, lookup.TenantID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.email_verifications SET revoked_at=$1 WHERE user_id=$2 AND used_at IS NULL AND revoked_at IS NULL`, now, command.UserID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO identity.email_verifications (id,user_id,token_hash,email,expires_at) VALUES ($1,$2,$3,$4,$5)`, prepared.VerificationID, command.UserID, prepared.TokenDigest, command.NewNormalizedEmail, prepared.ExpiresAt); err != nil {
			return idempotency.Response{}, err
		}
		var nextVersion uint64
		if err = tx.QueryRow(ctx, `UPDATE identity.users SET version=version+1,updated_at=$1 WHERE id=$2 AND version=$3 RETURNING version`, now, command.UserID, command.ExpectedUserVersion).Scan(&nextVersion); err != nil {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'email_change_requested',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, lookup.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"reauthenticated":true}`), now); err != nil {
			return idempotency.Response{}, err
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: lookup.TenantID, UserID: command.UserID, EventType: "EmailChangeRequested", SchemaVersion: 1, AggregateKind: "user", AggregateID: command.UserID, AggregateVersion: nextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.VerifyMailOutboxID, CommandID: prepared.VerifyMailCommandID, CommandType: "identity.email.change.verify", PayloadRef: prepared.VerifyMailCommand.Ref, PayloadHash: prepared.VerifyMailCommand.Hash}, {ID: prepared.SecurityMailOutboxID, CommandID: prepared.SecurityMailCommandID, CommandType: "identity.email.security", PayloadRef: prepared.SecurityMailCommand.Ref, PayloadHash: prepared.SecurityMailCommand.Hash}}})
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: nextVersion}, nil
	})
	if err != nil {
		return service.emailChangeReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readEmailChangeResponse(ctx, descriptor, response)
}

func (service AuthService) prepareEmailChangeRequest(ctx context.Context, lookup passwordChangeLookup, command api.EmailChangeCommand, recordID string, now time.Time) (emailChangeRequestPrepared, error) {
	var prepared emailChangeRequestPrepared
	for _, target := range []*string{&prepared.VerificationID, &prepared.SecurityEventID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID, &prepared.VerifyMailOutboxID, &prepared.VerifyMailCommandID, &prepared.SecurityMailOutboxID, &prepared.SecurityMailCommandID} {
		value, err := service.newID()
		if err != nil {
			return emailChangeRequestPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	manager := service.EmailChangeTokens
	if manager.Random == nil {
		manager.Random = service.random()
	}
	token, err := manager.Issue()
	if err != nil {
		return emailChangeRequestPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.TokenDigest = append([]byte(nil), token.Digest[:]...)
	prepared.ExpiresAt = now.Add(service.EmailChangeTTL)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": command.UserID, "subject_version": lookup.UserVersion + 1, "request_id": command.RequestID})
	if err != nil {
		return emailChangeRequestPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.VerifyMailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.VerifyMailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "confirm-email-change-v1", "locale": lookup.Locale, "recipient": command.NewNormalizedEmail, "token": token.Raw, "expires_at": prepared.ExpiresAt})
	if err != nil {
		return emailChangeRequestPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.SecurityMailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.SecurityMailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "email-change-requested-security-v1", "locale": lookup.Locale, "recipient": lookup.Email, "requested_at": now})
	if err != nil {
		return emailChangeRequestPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedEmailChangeResponse{ID: command.UserID, Version: lookup.UserVersion + 1, Status: "pending_confirmation", UpdatedAt: now})
	if err != nil {
		return emailChangeRequestPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

type emailConfirmationLookup struct {
	VerificationID, UserID, TenantID, OldEmail, NewEmail, Locale, Status string
	UserVersion                                                          uint64
	ExpiresAt                                                            time.Time
	UsedAt, RevokedAt                                                    *time.Time
	Session                                                              sessionRow
}

type emailConfirmationPrepared struct {
	SecurityEventID, EventID, PublishOutboxID, PublishCommandID          string
	OldMailOutboxID, OldMailCommandID, NewMailOutboxID, NewMailCommandID string
	SessionCredential, CSRFCredential                                    session.Credential
	ExpiresAt                                                            time.Time
	EventPayload, OldMailCommand, NewMailCommand, Response               payload.Manifest
}

type storedEmailConfirmationResponse struct {
	storedEmailChangeResponse
	SessionToken string    `json:"session_token"`
	CSRFToken    string    `json:"csrf_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (service AuthService) ConfirmEmailChange(ctx context.Context, command api.EmailConfirmChangeCommand) (api.EmailConfirmChangeResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.EmailConfirmChangeResult{}, err
	}
	if command.Token == "" {
		return api.EmailConfirmChangeResult{}, api.ErrValidation
	}
	digest, err := service.EmailChangeTokens.Digest(command.Token)
	if err != nil {
		return api.EmailConfirmChangeResult{}, api.ErrInvalidCredentials
	}
	recordID, err := service.idempotencyRecordID("auth.email_confirm_change", command.UserID, command.IdempotencyKey)
	if err != nil {
		return api.EmailConfirmChangeResult{}, api.ErrIdempotencyConflict
	}
	principalDigest := hex.EncodeToString(digest[:])
	canonical, _ := json.Marshal(struct {
		TokenDigest string `json:"token_digest"`
	}{principalDigest})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.EmailConfirmChangeResult{}, api.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "auth.email_confirm_change"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadEmailConfirmationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		return service.emailConfirmationReplayOr(ctx, input, descriptor, err)
	}
	lookup, err := service.lookupEmailConfirmation(ctx, command.UserID, command.SessionID, digest[:])
	if err != nil {
		return service.emailConfirmationReplayOr(ctx, input, descriptor, mapLookupError(err))
	}
	now := service.now()
	if lookup.TenantID != command.TenantID || lookup.Session.ActiveTenantID != command.TenantID || lookup.Status != "active" || lookup.UsedAt != nil || lookup.RevokedAt != nil || !lookup.ExpiresAt.After(now) || lookup.NewEmail == lookup.OldEmail {
		return service.emailConfirmationReplayOr(ctx, input, descriptor, api.ErrInvalidCredentials)
	}
	activeSessions, err := service.lookupActiveOwnedSessions(ctx, command.UserID)
	if err != nil {
		return api.EmailConfirmChangeResult{}, err
	}
	otherSessions := make([]sessionRevocationPrepared, 0, len(activeSessions))
	currentFound := false
	for _, row := range activeSessions {
		if row.ID == command.SessionID {
			currentFound = row.Version == lookup.Session.Version
			continue
		}
		prepared, prepareErr := service.prepareSessionRevocation(ctx, row, "email_changed")
		if prepareErr != nil {
			return api.EmailConfirmChangeResult{}, prepareErr
		}
		otherSessions = append(otherSessions, prepared)
	}
	if !currentFound {
		return service.emailConfirmationReplayOr(ctx, input, descriptor, api.ErrStateConflict)
	}
	prepared, err := service.prepareEmailConfirmation(ctx, lookup, command, recordID, now)
	if err != nil {
		return api.EmailConfirmChangeResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		var currentEmail, status string
		var userVersion uint64
		if err := tx.QueryRow(ctx, `SELECT normalized_email,status,version FROM identity.users WHERE id=$1 FOR UPDATE`, command.UserID).Scan(&currentEmail, &status, &userVersion); err != nil {
			return idempotency.Response{}, err
		}
		if err := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); err != nil {
			return idempotency.Response{}, err
		}
		var verification emailConfirmationLookup
		err := tx.QueryRow(ctx, `SELECT id::text,user_id::text,email,expires_at,used_at,revoked_at FROM identity.email_verifications WHERE id=$1 AND user_id=$2 FOR UPDATE`, lookup.VerificationID, command.UserID).Scan(&verification.VerificationID, &verification.UserID, &verification.NewEmail, &verification.ExpiresAt, &verification.UsedAt, &verification.RevokedAt)
		if err != nil || status != "active" || currentEmail != lookup.OldEmail || userVersion != lookup.UserVersion || verification.NewEmail != lookup.NewEmail || verification.UsedAt != nil || verification.RevokedAt != nil || !verification.ExpiresAt.After(now) {
			return idempotency.Response{}, api.ErrInvalidCredentials
		}
		lockedSessions, err := service.lockActiveOwnedSessions(ctx, tx, command.UserID)
		if err != nil {
			return idempotency.Response{}, err
		}
		actualOthers := make([]sessionRow, 0, len(lockedSessions))
		currentUnchanged := false
		for _, row := range lockedSessions {
			if row.ID == command.SessionID {
				currentUnchanged = row.Version == lookup.Session.Version
				continue
			}
			actualOthers = append(actualOthers, row)
		}
		if !currentUnchanged || !sameSessionSnapshot(otherSessions, actualOthers) {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.email_verifications SET used_at=$1 WHERE id=$2 AND used_at IS NULL AND revoked_at IS NULL`, now, lookup.VerificationID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.email_verifications SET revoked_at=$1 WHERE user_id=$2 AND id<>$3 AND used_at IS NULL AND revoked_at IS NULL`, now, command.UserID, lookup.VerificationID); err != nil {
			return idempotency.Response{}, err
		}
		var nextUserVersion uint64
		if err = tx.QueryRow(ctx, `UPDATE identity.users SET normalized_email=$1,email_verified_at=$2,version=version+1,updated_at=$2 WHERE id=$3 AND version=$4 RETURNING version`, lookup.NewEmail, now, command.UserID, lookup.UserVersion).Scan(&nextUserVersion); err != nil {
			return idempotency.Response{}, err
		}
		var nextSessionVersion uint64
		if err = tx.QueryRow(ctx, `UPDATE identity.sessions SET token_hash=$1,csrf_secret_hash=$2,last_seen_at=$3,expires_at=$4,reauthenticated_at=$3,version=version+1,updated_at=$3 WHERE id=$5 AND user_id=$6 AND version=$7 AND revoked_at IS NULL RETURNING version`, prepared.SessionCredential.Digest[:], prepared.CSRFCredential.Digest[:], now, prepared.ExpiresAt, command.SessionID, command.UserID, lookup.Session.Version).Scan(&nextSessionVersion); err != nil || nextSessionVersion != lookup.Session.Version+1 {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		if len(otherSessions) > 0 {
			if err = service.commitSessionRevocations(ctx, tx, command.AuthenticatedRequestMetadata, otherSessions, "email_changed", now, &command.UserID, actor); err != nil {
				return idempotency.Response{}, err
			}
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, lookup.TenantID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'email_changed',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, lookup.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"session_rotated":true}`), now); err != nil {
			return idempotency.Response{}, err
		}
		_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: lookup.TenantID, UserID: command.UserID, EventType: "EmailChanged", SchemaVersion: 1, AggregateKind: "user", AggregateID: command.UserID, AggregateVersion: nextUserVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.OldMailOutboxID, CommandID: prepared.OldMailCommandID, CommandType: "identity.email.security", PayloadRef: prepared.OldMailCommand.Ref, PayloadHash: prepared.OldMailCommand.Hash}, {ID: prepared.NewMailOutboxID, CommandID: prepared.NewMailCommandID, CommandType: "identity.email.security", PayloadRef: prepared.NewMailCommand.Ref, PayloadHash: prepared.NewMailCommand.Hash}}})
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: nextUserVersion}, nil
	})
	if err != nil {
		return service.emailConfirmationReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readEmailConfirmationResponse(ctx, descriptor, response)
}

func (service AuthService) prepareEmailConfirmation(ctx context.Context, lookup emailConfirmationLookup, command api.EmailConfirmChangeCommand, recordID string, now time.Time) (emailConfirmationPrepared, error) {
	var prepared emailConfirmationPrepared
	for _, target := range []*string{&prepared.SecurityEventID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID, &prepared.OldMailOutboxID, &prepared.OldMailCommandID, &prepared.NewMailOutboxID, &prepared.NewMailCommandID} {
		value, err := service.newID()
		if err != nil {
			return emailConfirmationPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	var err error
	prepared.SessionCredential, err = session.NewFrom(service.random(), service.SessionPepper)
	if err != nil {
		return emailConfirmationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.CSRFCredential, err = session.NewFrom(service.random(), service.CSRFPepper)
	if err != nil {
		return emailConfirmationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.ExpiresAt = now.Add(service.SessionTTL)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": lookup.UserID, "subject_version": lookup.UserVersion + 1, "previous_state": "verified", "new_state": "verified", "reason_code": "email_change_confirmed"})
	if err != nil {
		return emailConfirmationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.OldMailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.OldMailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "email-changed-old-address-security-v1", "locale": lookup.Locale, "recipient": lookup.OldEmail, "changed_at": now})
	if err != nil {
		return emailConfirmationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.NewMailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.NewMailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "email-changed-new-address-security-v1", "locale": lookup.Locale, "recipient": lookup.NewEmail, "changed_at": now})
	if err != nil {
		return emailConfirmationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedEmailConfirmationResponse{storedEmailChangeResponse: storedEmailChangeResponse{ID: lookup.UserID, Version: lookup.UserVersion + 1, Status: "changed", UpdatedAt: now}, SessionToken: prepared.SessionCredential.Raw, CSRFToken: prepared.CSRFCredential.Raw, ExpiresAt: prepared.ExpiresAt})
	if err != nil {
		return emailConfirmationPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) lookupEmailConfirmation(ctx context.Context, userID, sessionID string, digest []byte) (emailConfirmationLookup, error) {
	var value emailConfirmationLookup
	err := service.Pool.QueryRow(ctx, `WITH personal AS (SELECT owner_user_id,(array_agg(id ORDER BY created_at,id))[1]::text AS tenant_id,count(*) AS tenant_count FROM identity.tenants WHERE kind='personal' GROUP BY owner_user_id) SELECT ev.id::text,u.id::text,p.tenant_id,u.normalized_email,ev.email,u.locale,u.status,u.version,ev.expires_at,ev.used_at,ev.revoked_at,s.id::text,s.active_tenant_id::text,s.version,s.created_at,s.updated_at,s.last_seen_at,s.expires_at,s.revoked_at,s.device_label FROM identity.email_verifications ev JOIN identity.users u ON u.id=ev.user_id JOIN personal p ON p.owner_user_id=u.id AND p.tenant_count=1 JOIN identity.sessions s ON s.user_id=u.id WHERE ev.token_hash=$1 AND u.id=$2 AND s.id=$3`, digest, userID, sessionID).Scan(&value.VerificationID, &value.UserID, &value.TenantID, &value.OldEmail, &value.NewEmail, &value.Locale, &value.Status, &value.UserVersion, &value.ExpiresAt, &value.UsedAt, &value.RevokedAt, &value.Session.ID, &value.Session.ActiveTenantID, &value.Session.Version, &value.Session.CreatedAt, &value.Session.UpdatedAt, &value.Session.LastSeenAt, &value.Session.ExpiresAt, &value.Session.RevokedAt, &value.Session.DeviceLabel)
	if err != nil {
		return emailConfirmationLookup{}, err
	}
	return value, nil
}

func (service AuthService) readEmailChangeResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.EmailChangeResult, error) {
	var stored storedEmailChangeResponse
	if err := service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return api.EmailChangeResult{}, api.ErrDependencyUnavailable
	}
	return api.EmailChangeResult{ID: stored.ID, Version: stored.Version, Status: stored.Status, UpdatedAt: stored.UpdatedAt}, nil
}

func (service AuthService) loadEmailChangeReplay(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor) (api.EmailChangeResult, bool, error) {
	response, found, err := service.idempotencyExecutor().LoadCompleted(ctx, input)
	if err != nil {
		return api.EmailChangeResult{}, false, mapIdentityError(err)
	}
	if !found {
		return api.EmailChangeResult{}, false, nil
	}
	result, err := service.readEmailChangeResponse(ctx, descriptor, response)
	return result, true, err
}

func (service AuthService) emailChangeReplayOr(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor, fallback error) (api.EmailChangeResult, error) {
	if result, found, err := service.loadEmailChangeReplay(ctx, input, descriptor); err != nil || found {
		return result, err
	}
	return api.EmailChangeResult{}, fallback
}

func (service AuthService) readEmailConfirmationResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.EmailConfirmChangeResult, error) {
	var stored storedEmailConfirmationResponse
	if err := service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return api.EmailConfirmChangeResult{}, api.ErrDependencyUnavailable
	}
	return api.EmailConfirmChangeResult{EmailChangeResult: api.EmailChangeResult{ID: stored.ID, Version: stored.Version, Status: stored.Status, UpdatedAt: stored.UpdatedAt}, SessionToken: stored.SessionToken, CSRFToken: stored.CSRFToken, ExpiresAt: stored.ExpiresAt}, nil
}

func (service AuthService) loadEmailConfirmationReplay(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor) (api.EmailConfirmChangeResult, bool, error) {
	response, found, err := service.idempotencyExecutor().LoadCompleted(ctx, input)
	if err != nil {
		return api.EmailConfirmChangeResult{}, false, mapIdentityError(err)
	}
	if !found {
		return api.EmailConfirmChangeResult{}, false, nil
	}
	result, err := service.readEmailConfirmationResponse(ctx, descriptor, response)
	return result, true, err
}

func (service AuthService) emailConfirmationReplayOr(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor, fallback error) (api.EmailConfirmChangeResult, error) {
	if result, found, err := service.loadEmailConfirmationReplay(ctx, input, descriptor); err != nil || found {
		return result, err
	}
	return api.EmailConfirmChangeResult{}, fallback
}

var _ api.EmailService = AuthService{}
