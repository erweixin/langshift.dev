package postgres

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type passwordAccount struct {
	UserID, TenantID, CredentialID, Email, Locale, Status string
	UserVersion, CredentialVersion                        uint64
	PasswordHash                                          []byte
	PasswordParameters                                    password.Parameters
}

type passwordResetLookup struct {
	passwordAccount
	ResetID           string
	ExpiresAt         time.Time
	UsedAt, RevokedAt *time.Time
}

type forgotPasswordPrepared struct {
	ResetID, SecurityEventID, EventID                              string
	PublishOutboxID, PublishCommandID, MailOutboxID, MailCommandID string
	TokenDigest                                                    []byte
	ExpiresAt                                                      time.Time
	EventPayload, MailCommand, Response                            payload.Manifest
}

func (service AuthService) ForgotPassword(ctx context.Context, command api.PasswordForgotCommand) (api.PasswordForgotResult, error) {
	if err := service.validatePasswordPublic(command.RequestMetadata); err != nil {
		return api.PasswordForgotResult{}, err
	}
	if command.NormalizedEmail == "" {
		return api.PasswordForgotResult{}, api.ErrValidation
	}
	publicPrincipal, recordID, requestHash, err := service.publicIdempotency("auth.password_forgot", command.NormalizedEmail, command.IdempotencyKey, struct {
		Email string `json:"email"`
	}{command.NormalizedEmail})
	if err != nil {
		return api.PasswordForgotResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: service.PublicTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	responseManifest, err := service.putJSON(ctx, descriptor, map[string]string{"status": "accepted", "message": api.PasswordResetAcceptedMessage})
	if err != nil {
		return api.PasswordForgotResult{}, api.ErrDependencyUnavailable
	}
	account, found, err := service.lookupPasswordAccount(ctx, command.NormalizedEmail)
	if err != nil {
		return api.PasswordForgotResult{}, api.ErrDependencyUnavailable
	}
	idempotencyInput := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: service.PublicTenantID, UserID: publicPrincipal, OperationID: "auth.password_forgot"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if !found || account.Status != "active" {
		response, _, executeErr := service.idempotencyExecutor().Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
			eventID, idErr := service.newID()
			if idErr != nil {
				return idempotency.Response{}, idErr
			}
			details, _ := json.Marshal(map[string]string{"email_principal_id": publicPrincipal})
			if _, insertErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,'password_reset_requested',$3,$4,$5,$6,$7)`, eventID, service.PublicTenantID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, service.now()); insertErr != nil {
				return idempotency.Response{}, insertErr
			}
			return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: 1}, nil
		})
		if executeErr != nil {
			return api.PasswordForgotResult{}, mapIdentityError(executeErr)
		}
		return service.readForgotResponse(ctx, descriptor, response)
	}
	now := service.now()
	prepared, err := service.prepareForgotPassword(ctx, account, responseManifest, command, now)
	if err != nil {
		return api.PasswordForgotResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		var userVersion uint64
		if err := tx.QueryRow(ctx, `SELECT version FROM identity.users WHERE id=$1 AND status='active' AND email_verified_at IS NOT NULL FOR UPDATE`, account.UserID).Scan(&userVersion); err != nil {
			return idempotency.Response{}, err
		}
		var credentialVersion uint64
		if err := tx.QueryRow(ctx, `SELECT version FROM identity.password_credentials WHERE id=$1 AND user_id=$2 FOR UPDATE`, account.CredentialID, account.UserID).Scan(&credentialVersion); err != nil {
			return idempotency.Response{}, err
		}
		if userVersion != account.UserVersion || credentialVersion != account.CredentialVersion {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, account.TenantID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.password_reset_requests SET revoked_at=$1 WHERE user_id=$2 AND used_at IS NULL AND revoked_at IS NULL`, now, account.UserID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.password_reset_requests (id,user_id,token_hash,expires_at,request_ip_hash) VALUES ($1,$2,$3,$4,$5)`, prepared.ResetID, account.UserID, prepared.TokenDigest, prepared.ExpiresAt, command.ClientIPHash); err != nil {
			return idempotency.Response{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'password_reset_requested',$4,$5,$6,'{}',$7)`, prepared.SecurityEventID, account.TenantID, account.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, now); err != nil {
			return idempotency.Response{}, err
		}
		actor := json.RawMessage(`{"kind":"system","id":"identity-service"}`)
		_, err := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: account.TenantID, UserID: account.UserID, EventType: "PasswordResetRequested", SchemaVersion: 1, AggregateKind: "password_reset_request", AggregateID: prepared.ResetID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.MailOutboxID, CommandID: prepared.MailCommandID, CommandType: "identity.email.password_reset", PayloadRef: prepared.MailCommand.Ref, PayloadHash: prepared.MailCommand.Hash}}})
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return api.PasswordForgotResult{}, mapIdentityError(err)
	}
	return service.readForgotResponse(ctx, descriptor, response)
}

func (service AuthService) prepareForgotPassword(ctx context.Context, account passwordAccount, response payload.Manifest, command api.PasswordForgotCommand, now time.Time) (forgotPasswordPrepared, error) {
	prepared := forgotPasswordPrepared{ExpiresAt: now.Add(service.PasswordResetTTL), Response: response}
	for _, target := range []*string{&prepared.ResetID, &prepared.SecurityEventID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID, &prepared.MailOutboxID, &prepared.MailCommandID} {
		value, err := service.newID()
		if err != nil {
			return forgotPasswordPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	manager := service.PasswordResetTokens
	if manager.Random == nil {
		manager.Random = service.random()
	}
	token, err := manager.Issue()
	if err != nil {
		return forgotPasswordPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.TokenDigest = append([]byte(nil), token.Digest[:]...)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: account.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.ResetID, "subject_version": 1, "request_id": command.RequestID})
	if err != nil {
		return forgotPasswordPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.MailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: account.TenantID, ObjectID: prepared.MailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "password-reset-v1", "locale": account.Locale, "recipient": account.Email, "token": token.Raw, "expires_at": prepared.ExpiresAt})
	if err != nil {
		return forgotPasswordPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) ResetPassword(ctx context.Context, command api.PasswordResetCommand) (api.PasswordResetResult, error) {
	if err := service.validatePasswordPublic(command.RequestMetadata); err != nil {
		return api.PasswordResetResult{}, err
	}
	if command.Token == "" || command.NewPassword == "" {
		return api.PasswordResetResult{}, api.ErrValidation
	}
	digest, err := service.PasswordResetTokens.Digest(command.Token)
	if err != nil {
		return api.PasswordResetResult{}, api.ErrInvalidCredentials
	}
	lookup, err := service.lookupPasswordReset(ctx, digest[:])
	if err != nil {
		return api.PasswordResetResult{}, mapLookupError(err)
	}
	principalDigest := hex.EncodeToString(digest[:])
	publicPrincipal, err := ids.DeterministicUUID(service.IdentityKey, "public-password-reset", principalDigest)
	if err != nil {
		return api.PasswordResetResult{}, api.ErrDependencyUnavailable
	}
	recordID, err := service.idempotencyRecordID("auth.password_reset", publicPrincipal, command.IdempotencyKey)
	if err != nil {
		return api.PasswordResetResult{}, api.ErrIdempotencyConflict
	}
	canonical, _ := json.Marshal(struct {
		TokenDigest string `json:"token_digest"`
		NewPassword string `json:"new_password"`
	}{principalDigest, command.NewPassword})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.PasswordResetResult{}, api.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: service.PublicTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	idempotencyInput := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: service.PublicTenantID, UserID: publicPrincipal, OperationID: "auth.password_reset"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadPasswordResetReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	now := service.now()
	if lookup.UsedAt != nil || lookup.RevokedAt != nil || !lookup.ExpiresAt.After(now) || lookup.Status != "active" {
		if result, found, replayErr := service.loadPasswordResetReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
			return result, replayErr
		}
		return api.PasswordResetResult{}, api.ErrInvalidCredentials
	}
	same, verifyErr := service.Passwords.Verify(command.NewPassword, lookup.PasswordHash, lookup.PasswordParameters)
	if verifyErr != nil {
		return api.PasswordResetResult{}, api.ErrDependencyUnavailable
	}
	if same {
		return api.PasswordResetResult{}, api.ErrValidation
	}
	if err = service.validateNewPassword(ctx, command.NewPassword); err != nil {
		return api.PasswordResetResult{}, err
	}
	newHash, newParameters, err := service.Passwords.Hash(command.NewPassword)
	if err != nil {
		return api.PasswordResetResult{}, api.ErrValidation
	}
	encodedParameters, err := json.Marshal(newParameters)
	if err != nil {
		return api.PasswordResetResult{}, api.ErrDependencyUnavailable
	}
	sessions, err := service.lookupActiveOwnedSessions(ctx, lookup.UserID)
	if err != nil {
		return api.PasswordResetResult{}, err
	}
	preparedSessions := make([]sessionRevocationPrepared, 0, len(sessions))
	for _, row := range sessions {
		prepared, prepareErr := service.prepareSessionRevocation(ctx, row, "password_reset")
		if prepareErr != nil {
			return api.PasswordResetResult{}, prepareErr
		}
		preparedSessions = append(preparedSessions, prepared)
	}
	prepared, err := service.preparePasswordResetCompletion(ctx, lookup, recordID, len(preparedSessions), now)
	if err != nil {
		return api.PasswordResetResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		var lockedUserID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM identity.users WHERE id=$1 FOR UPDATE`, lookup.UserID).Scan(&lockedUserID); err != nil {
			return idempotency.Response{}, err
		}
		var current passwordResetLookup
		var parameters []byte
		err := tx.QueryRow(ctx, `SELECT pr.id::text,pr.user_id::text,pc.id::text,u.normalized_email,u.status,u.version,pc.version,pc.password_hash,pc.parameters,pr.expires_at,pr.used_at,pr.revoked_at FROM identity.password_reset_requests pr JOIN identity.users u ON u.id=pr.user_id JOIN identity.password_credentials pc ON pc.user_id=u.id WHERE pr.id=$1 FOR UPDATE OF pr,pc`, lookup.ResetID).Scan(&current.ResetID, &current.UserID, &current.CredentialID, &current.Email, &current.Status, &current.UserVersion, &current.CredentialVersion, &current.PasswordHash, &parameters, &current.ExpiresAt, &current.UsedAt, &current.RevokedAt)
		if err != nil || current.UserID != lookup.UserID || current.UserVersion != lookup.UserVersion || current.CredentialVersion != lookup.CredentialVersion || current.UsedAt != nil || current.RevokedAt != nil || !current.ExpiresAt.After(now) || current.Status != "active" {
			return idempotency.Response{}, api.ErrInvalidCredentials
		}
		lockedSessions, err := service.lockActiveOwnedSessions(ctx, tx, lookup.UserID)
		if err != nil {
			return idempotency.Response{}, err
		}
		if !sameSessionSnapshot(preparedSessions, lockedSessions) {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.password_reset_requests SET used_at=$1 WHERE id=$2 AND used_at IS NULL AND revoked_at IS NULL`, now, lookup.ResetID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.password_reset_requests SET revoked_at=$1 WHERE user_id=$2 AND id<>$3 AND used_at IS NULL AND revoked_at IS NULL`, now, lookup.UserID, lookup.ResetID); err != nil {
			return idempotency.Response{}, err
		}
		var nextVersion uint64
		if err = tx.QueryRow(ctx, `UPDATE identity.password_credentials SET password_hash=$1,parameters=$2,password_changed_at=$3,version=version+1,updated_at=$3 WHERE id=$4 AND version=$5 RETURNING version`, newHash, encodedParameters, now, lookup.CredentialID, lookup.CredentialVersion).Scan(&nextVersion); err != nil {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		metadata := api.AuthenticatedRequestMetadata{RequestMetadata: command.RequestMetadata, UserID: lookup.UserID, TenantID: lookup.TenantID}
		actor := json.RawMessage(`{"kind":"system","id":"identity-service"}`)
		if err = service.commitSessionRevocations(ctx, tx, metadata, preparedSessions, "password_reset", now, nil, actor); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, lookup.TenantID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'password_reset_completed',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, lookup.TenantID, lookup.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"sessions_revoked":true}`), now); err != nil {
			return idempotency.Response{}, err
		}
		_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: lookup.TenantID, UserID: lookup.UserID, EventType: "PasswordResetCompleted", SchemaVersion: 1, AggregateKind: "password_credential", AggregateID: lookup.CredentialID, AggregateVersion: nextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.MailOutboxID, CommandID: prepared.MailCommandID, CommandType: "identity.email.security", PayloadRef: prepared.MailCommand.Ref, PayloadHash: prepared.MailCommand.Hash}}})
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: nextVersion}, nil
	})
	if err != nil {
		return api.PasswordResetResult{}, mapIdentityError(err)
	}
	return service.readPasswordResetResponse(ctx, descriptor, response)
}

type passwordChangeLookup struct {
	passwordAccount
	Session sessionRow
}

type passwordChangePrepared struct {
	SecurityEventID, EventID, PublishOutboxID, PublishCommandID string
	MailOutboxID, MailCommandID                                 string
	SessionCredential, CSRFCredential                           session.Credential
	ExpiresAt                                                   time.Time
	EventPayload, MailCommand, Response                         payload.Manifest
}

type storedPasswordChangeResponse struct {
	ID           string    `json:"id"`
	Version      uint64    `json:"version"`
	Status       string    `json:"status"`
	UpdatedAt    time.Time `json:"updated_at"`
	SessionToken string    `json:"session_token"`
	CSRFToken    string    `json:"csrf_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (service AuthService) ChangePassword(ctx context.Context, command api.PasswordChangeCommand) (api.PasswordChangeResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.PasswordChangeResult{}, err
	}
	if command.CurrentPassword == "" || command.NewPassword == "" || command.CurrentPassword == command.NewPassword {
		return api.PasswordChangeResult{}, api.ErrValidation
	}
	recordID, err := service.idempotencyRecordID("auth.password_change", command.UserID, command.IdempotencyKey)
	if err != nil {
		return api.PasswordChangeResult{}, api.ErrIdempotencyConflict
	}
	canonical, _ := json.Marshal(struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}{command.CurrentPassword, command.NewPassword})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.PasswordChangeResult{}, api.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	idempotencyInput := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "auth.password_change"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadPasswordChangeReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		if result, found, replayErr := service.loadPasswordChangeReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
			return result, replayErr
		}
		return api.PasswordChangeResult{}, err
	}
	lookup, err := service.lookupPasswordChange(ctx, command.UserID, command.SessionID)
	if err != nil {
		return api.PasswordChangeResult{}, mapLookupError(err)
	}
	if lookup.Session.ActiveTenantID != command.TenantID || lookup.Status != "active" {
		if result, found, replayErr := service.loadPasswordChangeReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
			return result, replayErr
		}
		return api.PasswordChangeResult{}, api.ErrInvalidCredentials
	}
	verified, verifyErr := service.Passwords.Verify(command.CurrentPassword, lookup.PasswordHash, lookup.PasswordParameters)
	if verifyErr != nil {
		return api.PasswordChangeResult{}, api.ErrDependencyUnavailable
	}
	if !verified {
		if result, found, replayErr := service.loadPasswordChangeReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
			return result, replayErr
		}
		return api.PasswordChangeResult{}, api.ErrReauthenticationRequired
	}
	same, verifyErr := service.Passwords.Verify(command.NewPassword, lookup.PasswordHash, lookup.PasswordParameters)
	if verifyErr != nil {
		return api.PasswordChangeResult{}, api.ErrDependencyUnavailable
	}
	if same {
		if result, found, replayErr := service.loadPasswordChangeReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
			return result, replayErr
		}
		return api.PasswordChangeResult{}, api.ErrValidation
	}
	if err = service.validateNewPassword(ctx, command.NewPassword); err != nil {
		return api.PasswordChangeResult{}, err
	}
	newHash, newParameters, err := service.Passwords.Hash(command.NewPassword)
	if err != nil {
		return api.PasswordChangeResult{}, api.ErrValidation
	}
	encodedParameters, err := json.Marshal(newParameters)
	if err != nil {
		return api.PasswordChangeResult{}, api.ErrDependencyUnavailable
	}
	activeSessions, err := service.lookupActiveOwnedSessions(ctx, command.UserID)
	if err != nil {
		return api.PasswordChangeResult{}, err
	}
	otherSessions := make([]sessionRevocationPrepared, 0, len(activeSessions))
	currentFound := false
	for _, row := range activeSessions {
		if row.ID == command.SessionID {
			currentFound = row.Version == lookup.Session.Version
			continue
		}
		prepared, prepareErr := service.prepareSessionRevocation(ctx, row, "password_changed")
		if prepareErr != nil {
			return api.PasswordChangeResult{}, prepareErr
		}
		otherSessions = append(otherSessions, prepared)
	}
	if !currentFound {
		if result, found, replayErr := service.loadPasswordChangeReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
			return result, replayErr
		}
		return api.PasswordChangeResult{}, api.ErrStateConflict
	}
	now := service.now()
	prepared, err := service.preparePasswordChange(ctx, lookup, recordID, now)
	if err != nil {
		return api.PasswordChangeResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		var lockedUserID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM identity.users WHERE id=$1 FOR UPDATE`, command.UserID).Scan(&lockedUserID); err != nil {
			return idempotency.Response{}, err
		}
		if err := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); err != nil {
			return idempotency.Response{}, err
		}
		var credentialVersion uint64
		var storedHash []byte
		if err := tx.QueryRow(ctx, `SELECT version,password_hash FROM identity.password_credentials WHERE id=$1 AND user_id=$2 FOR UPDATE`, lookup.CredentialID, command.UserID).Scan(&credentialVersion, &storedHash); err != nil {
			return idempotency.Response{}, err
		}
		if credentialVersion != lookup.CredentialVersion || !hmac.Equal(storedHash, lookup.PasswordHash) {
			return idempotency.Response{}, api.ErrVersionConflict
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
		var nextCredentialVersion uint64
		if err = tx.QueryRow(ctx, `UPDATE identity.password_credentials SET password_hash=$1,parameters=$2,password_changed_at=$3,version=version+1,updated_at=$3 WHERE id=$4 AND version=$5 RETURNING version`, newHash, encodedParameters, now, lookup.CredentialID, lookup.CredentialVersion).Scan(&nextCredentialVersion); err != nil {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.password_reset_requests SET revoked_at=$1 WHERE user_id=$2 AND used_at IS NULL AND revoked_at IS NULL`, now, command.UserID); err != nil {
			return idempotency.Response{}, err
		}
		var nextSessionVersion uint64
		if err = tx.QueryRow(ctx, `UPDATE identity.sessions SET token_hash=$1,csrf_secret_hash=$2,last_seen_at=$3,expires_at=$4,reauthenticated_at=$3,version=version+1,updated_at=$3 WHERE id=$5 AND user_id=$6 AND version=$7 AND revoked_at IS NULL RETURNING version`, prepared.SessionCredential.Digest[:], prepared.CSRFCredential.Digest[:], now, prepared.ExpiresAt, command.SessionID, command.UserID, lookup.Session.Version).Scan(&nextSessionVersion); err != nil || nextSessionVersion != lookup.Session.Version+1 {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		if err = service.commitSessionRevocations(ctx, tx, command.AuthenticatedRequestMetadata, otherSessions, "password_changed", now, &command.UserID, actor); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, lookup.TenantID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'password_changed',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, lookup.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"session_rotated":true}`), now); err != nil {
			return idempotency.Response{}, err
		}
		_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: lookup.TenantID, UserID: command.UserID, EventType: "PasswordChanged", SchemaVersion: 1, AggregateKind: "password_credential", AggregateID: lookup.CredentialID, AggregateVersion: nextCredentialVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.MailOutboxID, CommandID: prepared.MailCommandID, CommandType: "identity.email.security", PayloadRef: prepared.MailCommand.Ref, PayloadHash: prepared.MailCommand.Hash}}})
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: nextCredentialVersion}, nil
	})
	if err != nil {
		return api.PasswordChangeResult{}, mapIdentityError(err)
	}
	return service.readPasswordChangeResponse(ctx, descriptor, response)
}

func (service AuthService) preparePasswordChange(ctx context.Context, lookup passwordChangeLookup, recordID string, now time.Time) (passwordChangePrepared, error) {
	var prepared passwordChangePrepared
	for _, target := range []*string{&prepared.SecurityEventID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID, &prepared.MailOutboxID, &prepared.MailCommandID} {
		value, err := service.newID()
		if err != nil {
			return passwordChangePrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	var err error
	prepared.SessionCredential, err = session.NewFrom(service.random(), service.SessionPepper)
	if err != nil {
		return passwordChangePrepared{}, api.ErrDependencyUnavailable
	}
	prepared.CSRFCredential, err = session.NewFrom(service.random(), service.CSRFPepper)
	if err != nil {
		return passwordChangePrepared{}, api.ErrDependencyUnavailable
	}
	prepared.ExpiresAt = now.Add(service.SessionTTL)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": lookup.CredentialID, "subject_version": lookup.CredentialVersion + 1, "previous_state": "active", "new_state": "changed", "reason_code": "authenticated_change"})
	if err != nil {
		return passwordChangePrepared{}, api.ErrDependencyUnavailable
	}
	prepared.MailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.MailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "password-changed-security-v1", "locale": lookup.Locale, "recipient": lookup.Email, "changed_at": now, "reason": "authenticated_change"})
	if err != nil {
		return passwordChangePrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.Session.ActiveTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedPasswordChangeResponse{ID: lookup.CredentialID, Version: lookup.CredentialVersion + 1, Status: "changed", UpdatedAt: now, SessionToken: prepared.SessionCredential.Raw, CSRFToken: prepared.CSRFCredential.Raw, ExpiresAt: prepared.ExpiresAt})
	if err != nil {
		return passwordChangePrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) lookupPasswordChange(ctx context.Context, userID, sessionID string) (passwordChangeLookup, error) {
	var value passwordChangeLookup
	var parameters []byte
	err := service.Pool.QueryRow(ctx, `WITH personal AS (SELECT owner_user_id,(array_agg(id ORDER BY created_at,id))[1]::text AS tenant_id,count(*) AS tenant_count FROM identity.tenants WHERE kind='personal' GROUP BY owner_user_id) SELECT u.id::text,p.tenant_id,pc.id::text,u.normalized_email,u.locale,u.status,u.version,pc.version,pc.password_hash,pc.parameters,s.id::text,s.active_tenant_id::text,s.version,s.created_at,s.updated_at,s.last_seen_at,s.expires_at,s.revoked_at,s.device_label FROM identity.users u JOIN identity.password_credentials pc ON pc.user_id=u.id JOIN personal p ON p.owner_user_id=u.id AND p.tenant_count=1 JOIN identity.sessions s ON s.user_id=u.id WHERE u.id=$1 AND s.id=$2`, userID, sessionID).Scan(&value.UserID, &value.TenantID, &value.CredentialID, &value.Email, &value.Locale, &value.Status, &value.UserVersion, &value.CredentialVersion, &value.PasswordHash, &parameters, &value.Session.ID, &value.Session.ActiveTenantID, &value.Session.Version, &value.Session.CreatedAt, &value.Session.UpdatedAt, &value.Session.LastSeenAt, &value.Session.ExpiresAt, &value.Session.RevokedAt, &value.Session.DeviceLabel)
	if err != nil {
		return passwordChangeLookup{}, err
	}
	if err = json.Unmarshal(parameters, &value.PasswordParameters); err != nil {
		return passwordChangeLookup{}, err
	}
	return value, nil
}

func (service AuthService) readPasswordChangeResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.PasswordChangeResult, error) {
	var stored storedPasswordChangeResponse
	if err := service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return api.PasswordChangeResult{}, api.ErrDependencyUnavailable
	}
	return api.PasswordChangeResult{ID: stored.ID, Version: stored.Version, Status: stored.Status, UpdatedAt: stored.UpdatedAt, SessionToken: stored.SessionToken, CSRFToken: stored.CSRFToken, ExpiresAt: stored.ExpiresAt}, nil
}

func (service AuthService) loadPasswordChangeReplay(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor) (api.PasswordChangeResult, bool, error) {
	response, found, err := service.idempotencyExecutor().LoadCompleted(ctx, input)
	if err != nil {
		return api.PasswordChangeResult{}, false, mapIdentityError(err)
	}
	if !found {
		return api.PasswordChangeResult{}, false, nil
	}
	result, err := service.readPasswordChangeResponse(ctx, descriptor, response)
	return result, true, err
}

type passwordResetCompletionPrepared struct {
	SecurityEventID, EventID, PublishOutboxID, PublishCommandID string
	MailOutboxID, MailCommandID                                 string
	EventPayload, MailCommand, Response                         payload.Manifest
}

func (service AuthService) preparePasswordResetCompletion(ctx context.Context, lookup passwordResetLookup, recordID string, revokedCount int, now time.Time) (passwordResetCompletionPrepared, error) {
	var prepared passwordResetCompletionPrepared
	for _, target := range []*string{&prepared.SecurityEventID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID, &prepared.MailOutboxID, &prepared.MailCommandID} {
		value, err := service.newID()
		if err != nil {
			return passwordResetCompletionPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	var err error
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": lookup.CredentialID, "subject_version": lookup.CredentialVersion + 1})
	if err != nil {
		return passwordResetCompletionPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.MailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.MailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "password-changed-security-v1", "locale": lookup.Locale, "recipient": lookup.Email, "changed_at": now, "reason": "password_reset"})
	if err != nil {
		return passwordResetCompletionPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: service.PublicTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, map[string]any{"revoked_session_count": revokedCount})
	if err != nil {
		return passwordResetCompletionPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) lookupPasswordAccount(ctx context.Context, normalizedEmail string) (passwordAccount, bool, error) {
	var value passwordAccount
	var parameters []byte
	var verifiedAt *time.Time
	err := service.Pool.QueryRow(ctx, `WITH personal AS (SELECT owner_user_id,(array_agg(id ORDER BY created_at,id))[1]::text AS tenant_id,count(*) AS tenant_count FROM identity.tenants WHERE kind='personal' GROUP BY owner_user_id) SELECT u.id::text,p.tenant_id,pc.id::text,u.normalized_email,u.locale,u.status,u.version,pc.version,pc.password_hash,pc.parameters,u.email_verified_at FROM identity.users u JOIN identity.password_credentials pc ON pc.user_id=u.id JOIN personal p ON p.owner_user_id=u.id AND p.tenant_count=1 WHERE u.normalized_email=$1`, normalizedEmail).Scan(&value.UserID, &value.TenantID, &value.CredentialID, &value.Email, &value.Locale, &value.Status, &value.UserVersion, &value.CredentialVersion, &value.PasswordHash, &parameters, &verifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return passwordAccount{}, false, nil
	}
	if err != nil {
		return passwordAccount{}, false, err
	}
	if verifiedAt == nil {
		return passwordAccount{}, false, nil
	}
	if err = json.Unmarshal(parameters, &value.PasswordParameters); err != nil {
		return passwordAccount{}, false, err
	}
	return value, true, nil
}

func (service AuthService) lookupPasswordReset(ctx context.Context, digest []byte) (passwordResetLookup, error) {
	var value passwordResetLookup
	var parameters []byte
	err := service.Pool.QueryRow(ctx, `WITH personal AS (SELECT owner_user_id,(array_agg(id ORDER BY created_at,id))[1]::text AS tenant_id,count(*) AS tenant_count FROM identity.tenants WHERE kind='personal' GROUP BY owner_user_id) SELECT pr.id::text,u.id::text,p.tenant_id,pc.id::text,u.normalized_email,u.locale,u.status,u.version,pc.version,pc.password_hash,pc.parameters,pr.expires_at,pr.used_at,pr.revoked_at FROM identity.password_reset_requests pr JOIN identity.users u ON u.id=pr.user_id JOIN identity.password_credentials pc ON pc.user_id=u.id JOIN personal p ON p.owner_user_id=u.id AND p.tenant_count=1 WHERE pr.token_hash=$1`, digest).Scan(&value.ResetID, &value.UserID, &value.TenantID, &value.CredentialID, &value.Email, &value.Locale, &value.Status, &value.UserVersion, &value.CredentialVersion, &value.PasswordHash, &parameters, &value.ExpiresAt, &value.UsedAt, &value.RevokedAt)
	if err != nil {
		return passwordResetLookup{}, err
	}
	if err = json.Unmarshal(parameters, &value.PasswordParameters); err != nil {
		return passwordResetLookup{}, err
	}
	return value, nil
}

func (service AuthService) readForgotResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.PasswordForgotResult, error) {
	var result api.PasswordForgotResult
	if err := service.readResponse(ctx, descriptor, response, &result); err != nil {
		return api.PasswordForgotResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

func (service AuthService) readPasswordResetResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.PasswordResetResult, error) {
	var result struct {
		RevokedSessionCount int `json:"revoked_session_count"`
	}
	if err := service.readResponse(ctx, descriptor, response, &result); err != nil {
		return api.PasswordResetResult{}, api.ErrDependencyUnavailable
	}
	return api.PasswordResetResult{RevokedSessionCount: result.RevokedSessionCount}, nil
}

func (service AuthService) loadPasswordResetReplay(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor) (api.PasswordResetResult, bool, error) {
	response, found, err := service.idempotencyExecutor().LoadCompleted(ctx, input)
	if err != nil {
		return api.PasswordResetResult{}, false, mapIdentityError(err)
	}
	if !found {
		return api.PasswordResetResult{}, false, nil
	}
	result, err := service.readPasswordResetResponse(ctx, descriptor, response)
	return result, true, err
}

func (service AuthService) validatePasswordPublic(metadata api.RequestMetadata) error {
	if err := service.validate(); err != nil {
		return api.ErrDependencyUnavailable
	}
	if metadata.RequestID == "" || metadata.ClientRequestID == "" || metadata.IdempotencyKey == "" || len(metadata.ClientIPHash) != 32 || len(metadata.UserAgentHash) != 32 {
		return api.ErrValidation
	}
	return nil
}

var _ api.PasswordService = AuthService{}
