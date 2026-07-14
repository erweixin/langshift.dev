package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/security/opaque"
)

type AuthService struct {
	Pool                    *pgxpool.Pool
	Passwords               password.Hasher
	PasswordPolicy          password.Policy
	DummyPasswordHash       []byte
	DummyPasswordParameters password.Parameters
	VerificationTokens      opaque.Manager
	PasswordResetTokens     opaque.Manager
	SessionPepper           []byte
	CSRFPepper              []byte
	IdempotencyKeyPepper    []byte
	RequestDigestPepper     []byte
	IdentityKey             []byte
	CursorKey               []byte
	PublicTenantID          string
	StoreEpoch              string
	Region                  string
	VerificationTTL         time.Duration
	PasswordResetTTL        time.Duration
	SessionTTL              time.Duration
	IdempotencyTTL          time.Duration
	Payloads                payload.Store
	Appender                eventpostgres.Appender
	Random                  io.Reader
	Now                     func() time.Time
}

type registrationPrepared struct {
	UserID, TenantID, MembershipID, CredentialID, VerificationID, SecurityEventID  string
	UserEventID, UserOutboxID, UserCommandID                                       string
	VerificationEventID, VerificationPublishOutboxID, VerificationPublishCommandID string
	VerificationMailOutboxID, VerificationMailCommandID                            string
	PasswordHash                                                                   []byte
	PasswordParameters                                                             []byte
	VerificationDigest                                                             []byte
	VerificationExpiresAt                                                          time.Time
	UserEventPayload, VerificationEventPayload, MailCommand, Response              payload.Manifest
}

func (service AuthService) Register(ctx context.Context, command api.RegisterCommand) (api.RegisterResult, error) {
	if err := service.validate(); err != nil {
		return api.RegisterResult{}, api.ErrDependencyUnavailable
	}
	now := service.now()
	canonicalRequest := struct {
		ClientRequestID string `json:"request_id"`
		Email           string `json:"email"`
		Password        string `json:"password"`
		Locale          string `json:"locale"`
	}{ClientRequestID: command.ClientRequestID, Email: command.NormalizedEmail, Password: command.Password, Locale: command.Locale}
	publicPrincipal, recordID, requestHash, err := service.publicIdempotency("auth.register", command.NormalizedEmail, command.IdempotencyKey, canonicalRequest)
	if err != nil {
		return api.RegisterResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: service.PublicTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	idempotencyInput := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: service.PublicTenantID, UserID: publicPrincipal, OperationID: "auth.register"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if response, found, replayErr := service.idempotencyExecutor().LoadCompleted(ctx, idempotencyInput); replayErr != nil {
		return api.RegisterResult{}, mapIdentityError(replayErr)
	} else if found {
		return service.readRegisterResponse(ctx, descriptor, response)
	}
	if err = service.validateNewPassword(ctx, command.Password); err != nil {
		return api.RegisterResult{}, err
	}
	prepared, err := service.prepareRegistration(ctx, command, recordID, now)
	if err != nil {
		return api.RegisterResult{}, err
	}
	executor := service.idempotencyExecutor()
	response, _, err := executor.Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		return service.commitRegistration(ctx, tx, command, prepared, now)
	})
	if err != nil {
		return api.RegisterResult{}, mapIdentityError(err)
	}
	return service.readRegisterResponse(ctx, descriptor, response)
}

func (service AuthService) readRegisterResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.RegisterResult, error) {
	var stored struct {
		UserID    string    `json:"user_id"`
		ExpiresAt time.Time `json:"email_verification_expires_at"`
	}
	if err := service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return api.RegisterResult{}, api.ErrDependencyUnavailable
	}
	return api.RegisterResult{UserID: stored.UserID, EmailVerificationExpiresAt: stored.ExpiresAt}, nil
}

func (service AuthService) VerifyEmail(ctx context.Context, command api.VerifyEmailCommand) (api.VerifyEmailResult, error) {
	if err := service.validate(); err != nil {
		return api.VerifyEmailResult{}, api.ErrDependencyUnavailable
	}
	digest, err := service.VerificationTokens.Digest(command.Token)
	if err != nil {
		return api.VerifyEmailResult{}, api.ErrInvalidCredentials
	}
	lookup, err := service.lookupVerification(ctx, digest[:])
	if err != nil {
		return api.VerifyEmailResult{}, mapLookupError(err)
	}
	principal := hex.EncodeToString(digest[:])
	publicPrincipal, err := ids.DeterministicUUID(service.IdentityKey, "public-verify-email", principal)
	if err != nil {
		return api.VerifyEmailResult{}, api.ErrDependencyUnavailable
	}
	recordID, err := service.idempotencyRecordID("auth.verify_email", publicPrincipal, command.IdempotencyKey)
	if err != nil {
		return api.VerifyEmailResult{}, api.ErrIdempotencyConflict
	}
	canonical, _ := json.Marshal(struct {
		TokenDigest string `json:"token_digest"`
	}{TokenDigest: principal})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.VerifyEmailResult{}, api.ErrDependencyUnavailable
	}
	now := service.now()
	prepared, err := service.prepareVerification(ctx, lookup, recordID, now)
	if err != nil {
		return api.VerifyEmailResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: service.PublicTenantID, UserID: publicPrincipal, OperationID: "auth.verify_email"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		return service.commitVerification(ctx, tx, command, lookup, prepared, now)
	})
	if err != nil {
		return api.VerifyEmailResult{}, mapIdentityError(err)
	}
	descriptor := payload.Descriptor{TenantID: service.PublicTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	var stored struct {
		UserID     string    `json:"user_id"`
		VerifiedAt time.Time `json:"verified_at"`
	}
	if err = service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return api.VerifyEmailResult{}, api.ErrDependencyUnavailable
	}
	return api.VerifyEmailResult{UserID: stored.UserID, VerifiedAt: stored.VerifiedAt}, nil
}

func (service AuthService) Login(ctx context.Context, command api.LoginCommand) (api.LoginResult, error) {
	if err := service.validate(); err != nil {
		return api.LoginResult{}, api.ErrDependencyUnavailable
	}
	lookup, found, err := service.lookupLogin(ctx, command.NormalizedEmail)
	if err != nil {
		return api.LoginResult{}, api.ErrDependencyUnavailable
	}
	digest := service.DummyPasswordHash
	parameters := service.DummyPasswordParameters
	if found {
		digest, parameters = lookup.PasswordHash, lookup.PasswordParameters
	}
	verified, verifyErr := service.Passwords.Verify(command.Password, digest, parameters)
	if verifyErr != nil || !verified || !found || lookup.Status != "active" || !lookup.EmailVerified {
		if auditErr := service.recordLoginFailure(ctx, command); auditErr != nil {
			return api.LoginResult{}, api.ErrDependencyUnavailable
		}
		return api.LoginResult{}, api.ErrInvalidCredentials
	}
	recordID, err := service.idempotencyRecordID("auth.login", lookup.UserID, command.IdempotencyKey)
	if err != nil {
		return api.LoginResult{}, api.ErrIdempotencyConflict
	}
	canonical, _ := json.Marshal(struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}{Email: command.NormalizedEmail, Password: command.Password})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.LoginResult{}, api.ErrDependencyUnavailable
	}
	now := service.now()
	prepared, err := service.prepareLogin(ctx, lookup, recordID, now)
	if err != nil {
		return api.LoginResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: lookup.TenantID, UserID: lookup.UserID, OperationID: "auth.login"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		return service.commitLogin(ctx, tx, command, lookup, prepared, now)
	})
	if err != nil {
		return api.LoginResult{}, mapIdentityError(err)
	}
	descriptor := payload.Descriptor{TenantID: lookup.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	var stored struct {
		UserID       string    `json:"user_id"`
		SessionID    string    `json:"session_id"`
		SessionToken string    `json:"session_token"`
		CSRFToken    string    `json:"csrf_token"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if err = service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return api.LoginResult{}, api.ErrDependencyUnavailable
	}
	return api.LoginResult{UserID: stored.UserID, SessionID: stored.SessionID, SessionToken: stored.SessionToken, CSRFToken: stored.CSRFToken, ExpiresAt: stored.ExpiresAt}, nil
}

func (service AuthService) prepareRegistration(ctx context.Context, command api.RegisterCommand, recordID string, now time.Time) (registrationPrepared, error) {
	var prepared registrationPrepared
	var err error
	idsOut := []*string{&prepared.UserID, &prepared.TenantID, &prepared.MembershipID, &prepared.CredentialID, &prepared.VerificationID, &prepared.SecurityEventID, &prepared.UserEventID, &prepared.UserOutboxID, &prepared.UserCommandID, &prepared.VerificationEventID, &prepared.VerificationPublishOutboxID, &prepared.VerificationPublishCommandID, &prepared.VerificationMailOutboxID, &prepared.VerificationMailCommandID}
	for _, target := range idsOut {
		if *target, err = service.newID(); err != nil {
			return registrationPrepared{}, api.ErrDependencyUnavailable
		}
	}
	var passwordParameters password.Parameters
	prepared.PasswordHash, passwordParameters, err = service.Passwords.Hash(command.Password)
	if err != nil {
		return registrationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.PasswordParameters, err = json.Marshal(passwordParameters)
	if err != nil {
		return registrationPrepared{}, api.ErrDependencyUnavailable
	}
	manager := service.VerificationTokens
	if manager.Random == nil {
		manager.Random = service.random()
	}
	verificationToken, err := manager.Issue()
	if err != nil {
		return registrationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.VerificationDigest = append([]byte(nil), verificationToken.Digest[:]...)
	prepared.VerificationExpiresAt = now.Add(service.VerificationTTL)
	prepared.UserEventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: prepared.TenantID, ObjectID: prepared.UserEventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.UserID, "subject_version": 1})
	if err != nil {
		return registrationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.VerificationEventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: prepared.TenantID, ObjectID: prepared.VerificationEventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.VerificationID, "subject_version": 1, "request_id": command.RequestID})
	if err != nil {
		return registrationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.MailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: prepared.TenantID, ObjectID: prepared.VerificationMailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "verify-email-v1", "locale": command.Locale, "recipient": command.NormalizedEmail, "token": verificationToken.Raw, "expires_at": prepared.VerificationExpiresAt})
	if err != nil {
		return registrationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: service.PublicTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, map[string]any{"user_id": prepared.UserID, "email_verification_expires_at": prepared.VerificationExpiresAt})
	if err != nil {
		return registrationPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) commitRegistration(ctx context.Context, tx pgx.Tx, command api.RegisterCommand, prepared registrationPrepared, now time.Time) (idempotency.Response, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO identity.users (id,normalized_email,locale,status) VALUES ($1,$2,$3,'pending_verification')`, prepared.UserID, command.NormalizedEmail, command.Locale); err != nil {
		return idempotency.Response{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.password_credentials (id,user_id,password_hash,algorithm,parameters,password_changed_at) VALUES ($1,$2,$3,'argon2id',$4,$5)`, prepared.CredentialID, prepared.UserID, prepared.PasswordHash, prepared.PasswordParameters, now); err != nil {
		return idempotency.Response{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Personal','active',$2,$3)`, prepared.TenantID, service.Region, prepared.UserID); err != nil {
		return idempotency.Response{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, prepared.TenantID); err != nil {
		return idempotency.Response{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'owner','active',$4)`, prepared.MembershipID, prepared.TenantID, prepared.UserID, now); err != nil {
		return idempotency.Response{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.email_verifications (id,user_id,token_hash,email,expires_at) VALUES ($1,$2,$3,$4,$5)`, prepared.VerificationID, prepared.UserID, prepared.VerificationDigest, command.NormalizedEmail, prepared.VerificationExpiresAt); err != nil {
		return idempotency.Response{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'user_registered',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, prepared.TenantID, prepared.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"channel":"email_password"}`), now); err != nil {
		return idempotency.Response{}, err
	}
	actor := json.RawMessage(`{"kind":"system","id":"identity-service"}`)
	if _, err := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.UserEventID, TenantID: prepared.TenantID, UserID: prepared.UserID, EventType: "UserRegistered", SchemaVersion: 1, AggregateKind: "user", AggregateID: prepared.UserID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.UserEventID, PayloadRef: prepared.UserEventPayload.Ref, PayloadHash: prepared.UserEventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.UserOutboxID, CommandID: prepared.UserCommandID, CommandType: "events.publish", PayloadRef: prepared.UserEventPayload.Ref, PayloadHash: prepared.UserEventPayload.Hash}}}); err != nil {
		return idempotency.Response{}, err
	}
	if _, err := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.VerificationEventID, TenantID: prepared.TenantID, UserID: prepared.UserID, EventType: "EmailVerificationRequested", SchemaVersion: 1, AggregateKind: "email_verification", AggregateID: prepared.VerificationID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.UserEventID, PayloadRef: prepared.VerificationEventPayload.Ref, PayloadHash: prepared.VerificationEventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.VerificationPublishOutboxID, CommandID: prepared.VerificationPublishCommandID, CommandType: "events.publish", PayloadRef: prepared.VerificationEventPayload.Ref, PayloadHash: prepared.VerificationEventPayload.Hash}, {ID: prepared.VerificationMailOutboxID, CommandID: prepared.VerificationMailCommandID, CommandType: "identity.email.verify", PayloadRef: prepared.MailCommand.Ref, PayloadHash: prepared.MailCommand.Hash}}}); err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: 1}, nil
}

type verificationLookup struct {
	VerificationID, UserID, TenantID, Status string
	UserVersion                              uint64
	ExpiresAt                                time.Time
	UsedAt, RevokedAt                        *time.Time
}

type verificationPrepared struct {
	SecurityEventID, EventID, OutboxID, CommandID string
	EventPayload, Response                        payload.Manifest
}

func (service AuthService) lookupVerification(ctx context.Context, digest []byte) (verificationLookup, error) {
	var value verificationLookup
	err := service.Pool.QueryRow(ctx, `WITH personal AS (SELECT owner_user_id,(array_agg(id ORDER BY created_at,id))[1]::text AS tenant_id,count(*) AS tenant_count FROM identity.tenants WHERE kind='personal' GROUP BY owner_user_id) SELECT ev.id::text,ev.user_id::text,p.tenant_id,u.status,u.version,ev.expires_at,ev.used_at,ev.revoked_at FROM identity.email_verifications ev JOIN identity.users u ON u.id=ev.user_id JOIN personal p ON p.owner_user_id=u.id AND p.tenant_count=1 WHERE ev.token_hash=$1`, digest).Scan(&value.VerificationID, &value.UserID, &value.TenantID, &value.Status, &value.UserVersion, &value.ExpiresAt, &value.UsedAt, &value.RevokedAt)
	return value, err
}

func (service AuthService) prepareVerification(ctx context.Context, lookup verificationLookup, recordID string, now time.Time) (verificationPrepared, error) {
	var prepared verificationPrepared
	for _, target := range []*string{&prepared.SecurityEventID, &prepared.EventID, &prepared.OutboxID, &prepared.CommandID} {
		value, err := service.newID()
		if err != nil {
			return verificationPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	var err error
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": lookup.UserID, "subject_version": lookup.UserVersion + 1})
	if err != nil {
		return verificationPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: service.PublicTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, map[string]any{"user_id": lookup.UserID, "verified_at": now})
	if err != nil {
		return verificationPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) commitVerification(ctx context.Context, tx pgx.Tx, command api.VerifyEmailCommand, expected verificationLookup, prepared verificationPrepared, now time.Time) (idempotency.Response, error) {
	var current verificationLookup
	err := tx.QueryRow(ctx, `WITH personal AS (SELECT owner_user_id,(array_agg(id ORDER BY created_at,id))[1]::text AS tenant_id,count(*) AS tenant_count FROM identity.tenants WHERE kind='personal' GROUP BY owner_user_id) SELECT ev.id::text,ev.user_id::text,p.tenant_id,u.status,u.version,ev.expires_at,ev.used_at,ev.revoked_at FROM identity.email_verifications ev JOIN identity.users u ON u.id=ev.user_id JOIN personal p ON p.owner_user_id=u.id AND p.tenant_count=1 WHERE ev.id=$1 FOR UPDATE OF ev,u`, expected.VerificationID).Scan(&current.VerificationID, &current.UserID, &current.TenantID, &current.Status, &current.UserVersion, &current.ExpiresAt, &current.UsedAt, &current.RevokedAt)
	if err != nil || current.UserID != expected.UserID || current.TenantID != expected.TenantID || current.UsedAt != nil || current.RevokedAt != nil || !current.ExpiresAt.After(now) || current.Status != "pending_verification" || current.UserVersion != expected.UserVersion {
		return idempotency.Response{}, api.ErrInvalidCredentials
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.email_verifications SET used_at=$1 WHERE id=$2 AND used_at IS NULL AND revoked_at IS NULL`, now, current.VerificationID); err != nil {
		return idempotency.Response{}, err
	}
	var nextVersion uint64
	err = tx.QueryRow(ctx, `UPDATE identity.users SET email_verified_at=$1,status='active',version=version+1,updated_at=$1 WHERE id=$2 AND version=$3 RETURNING version`, now, current.UserID, current.UserVersion).Scan(&nextVersion)
	if err != nil {
		return idempotency.Response{}, api.ErrStateConflict
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, current.TenantID); err != nil {
		return idempotency.Response{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'email_verified',$4,$5,$6,'{}',$7)`, prepared.SecurityEventID, current.TenantID, current.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, now); err != nil {
		return idempotency.Response{}, err
	}
	actor := json.RawMessage(`{"kind":"user","id":"` + current.UserID + `"}`)
	if _, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: current.TenantID, UserID: current.UserID, EventType: "EmailVerified", SchemaVersion: 1, AggregateKind: "user", AggregateID: current.UserID, AggregateVersion: nextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.EventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.OutboxID, CommandID: prepared.CommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}}}); err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: nextVersion}, nil
}

type loginLookup struct {
	UserID, TenantID, Status string
	EmailVerified            bool
	PasswordHash             []byte
	PasswordParameters       password.Parameters
	CredentialVersion        uint64
}

func (service AuthService) lookupLogin(ctx context.Context, normalizedEmail string) (loginLookup, bool, error) {
	var value loginLookup
	var parameters []byte
	var verifiedAt *time.Time
	err := service.Pool.QueryRow(ctx, `WITH personal AS (SELECT owner_user_id,(array_agg(id ORDER BY created_at,id))[1]::text AS tenant_id,count(*) AS tenant_count FROM identity.tenants WHERE kind='personal' GROUP BY owner_user_id) SELECT u.id::text,p.tenant_id,u.status,u.email_verified_at,pc.password_hash,pc.parameters,pc.version FROM identity.users u JOIN identity.password_credentials pc ON pc.user_id=u.id JOIN personal p ON p.owner_user_id=u.id AND p.tenant_count=1 WHERE u.normalized_email=$1`, normalizedEmail).Scan(&value.UserID, &value.TenantID, &value.Status, &verifiedAt, &value.PasswordHash, &parameters, &value.CredentialVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return loginLookup{}, false, nil
	}
	if err != nil {
		return loginLookup{}, false, err
	}
	if err = json.Unmarshal(parameters, &value.PasswordParameters); err != nil {
		return loginLookup{}, false, err
	}
	value.EmailVerified = verifiedAt != nil
	return value, true, nil
}

type loginPrepared struct {
	SessionID, SecurityEventID, EventID, OutboxID, CommandID string
	SessionCredential, CSRFCredential                        session.Credential
	ExpiresAt                                                time.Time
	EventPayload, Response                                   payload.Manifest
}

func (service AuthService) prepareLogin(ctx context.Context, lookup loginLookup, recordID string, now time.Time) (loginPrepared, error) {
	var prepared loginPrepared
	for _, target := range []*string{&prepared.SessionID, &prepared.SecurityEventID, &prepared.EventID, &prepared.OutboxID, &prepared.CommandID} {
		value, err := service.newID()
		if err != nil {
			return loginPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	var err error
	prepared.SessionCredential, err = session.NewFrom(service.random(), service.SessionPepper)
	if err != nil {
		return loginPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.CSRFCredential, err = session.NewFrom(service.random(), service.CSRFPepper)
	if err != nil {
		return loginPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.ExpiresAt = now.Add(service.SessionTTL)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.SessionID, "subject_version": 1})
	if err != nil {
		return loginPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: lookup.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, map[string]any{"user_id": lookup.UserID, "session_id": prepared.SessionID, "session_token": prepared.SessionCredential.Raw, "csrf_token": prepared.CSRFCredential.Raw, "expires_at": prepared.ExpiresAt})
	if err != nil {
		return loginPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) commitLogin(ctx context.Context, tx pgx.Tx, command api.LoginCommand, expected loginLookup, prepared loginPrepared, now time.Time) (idempotency.Response, error) {
	var lockedUserID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM identity.users WHERE id=$1 AND status='active' AND email_verified_at IS NOT NULL FOR UPDATE`, expected.UserID).Scan(&lockedUserID); errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Response{}, api.ErrInvalidCredentials
	} else if err != nil {
		return idempotency.Response{}, err
	}
	var credentialVersion uint64
	var storedHash []byte
	err := tx.QueryRow(ctx, `SELECT version,password_hash FROM identity.password_credentials WHERE user_id=$1 FOR UPDATE`, expected.UserID).Scan(&credentialVersion, &storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Response{}, api.ErrInvalidCredentials
	}
	if err != nil {
		return idempotency.Response{}, err
	}
	if credentialVersion != expected.CredentialVersion || !hmac.Equal(storedHash, expected.PasswordHash) {
		return idempotency.Response{}, api.ErrInvalidCredentials
	}
	var membershipID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2 AND status='active' FOR UPDATE`, expected.TenantID, expected.UserID).Scan(&membershipID)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Response{}, api.ErrInvalidCredentials
	}
	if err != nil {
		return idempotency.Response{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.sessions (id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)`, prepared.SessionID, expected.UserID, expected.TenantID, prepared.SessionCredential.Digest[:], prepared.CSRFCredential.Digest[:], command.ClientIPHash, command.UserAgentHash, now, prepared.ExpiresAt); err != nil {
		return idempotency.Response{}, err
	}
	details, _ := json.Marshal(map[string]string{"membership_id": membershipID})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'login_succeeded',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, expected.TenantID, expected.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, now); err != nil {
		return idempotency.Response{}, err
	}
	actor := json.RawMessage(`{"kind":"user","id":"` + expected.UserID + `"}`)
	if _, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: expected.TenantID, UserID: expected.UserID, EventType: "SessionCreated", SchemaVersion: 1, AggregateKind: "session", AggregateID: prepared.SessionID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.EventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.OutboxID, CommandID: prepared.CommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}}}); err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: 1}, nil
}

func (service AuthService) recordLoginFailure(ctx context.Context, command api.LoginCommand) error {
	eventID, err := service.newID()
	if err != nil {
		return err
	}
	principal, err := ids.DeterministicUUID(service.IdentityKey, "login-email", command.NormalizedEmail)
	if err != nil {
		return err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, service.PublicTenantID); err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]string{"email_principal_id": principal})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,'login_failed',$3,$4,$5,$6,$7)`, eventID, service.PublicTenantID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, service.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service AuthService) publicIdempotency(operation, principalValue, rawKey string, request any) (string, string, string, error) {
	principal, err := ids.DeterministicUUID(service.IdentityKey, "public-principal:"+operation, principalValue)
	if err != nil {
		return "", "", "", err
	}
	recordID, err := service.idempotencyRecordID(operation, principal, rawKey)
	if err != nil {
		return "", "", "", err
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return "", "", "", err
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	return principal, recordID, requestHash, err
}

func (service AuthService) idempotencyRecordID(operation, principal, rawKey string) (string, error) {
	return ids.DeterministicUUID(service.IdentityKey, "idempotency-record:"+operation, principal+"\x00"+rawKey)
}

func (service AuthService) idempotencyExecutor() IdempotencyExecutor {
	return IdempotencyExecutor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
}

func (service AuthService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service AuthService) readResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response, target any) error {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("identity idempotency response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func (service AuthService) newID() (string, error) { return ids.NewUUIDFrom(service.random()) }

func (service AuthService) random() io.Reader {
	if service.Random != nil {
		return service.Random
	}
	return rand.Reader
}

func (service AuthService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func (service AuthService) validate() error {
	if service.Pool == nil || service.Payloads == nil || !service.PasswordPolicy.Configured() || service.PublicTenantID == "" || service.StoreEpoch == "" || service.Region == "" || service.VerificationTTL <= 0 || service.PasswordResetTTL <= 0 || service.SessionTTL <= 0 || service.IdempotencyTTL <= 0 || len(service.IdentityKey) < 32 || len(service.CursorKey) < 32 || len(service.SessionPepper) < 32 || len(service.CSRFPepper) < 32 || len(service.IdempotencyKeyPepper) < 32 || len(service.RequestDigestPepper) < 32 || service.PasswordResetTokens.Purpose == "" || len(service.PasswordResetTokens.Pepper) < 32 || bytes.Equal(service.SessionPepper, service.CSRFPepper) || len(service.DummyPasswordHash) == 0 {
		return errors.New("identity auth service configuration is invalid")
	}
	return nil
}

func (service AuthService) validateNewPassword(ctx context.Context, value string) error {
	err := service.PasswordPolicy.ValidateNew(ctx, value)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, password.ErrInvalidPassword), errors.Is(err, password.ErrCompromisedPassword):
		return api.ErrValidation
	default:
		return api.ErrDependencyUnavailable
	}
}

func mapLookupError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return api.ErrInvalidCredentials
	}
	return api.ErrDependencyUnavailable
}

func mapIdentityError(err error) error {
	if errors.Is(err, api.ErrInvalidCredentials) {
		return api.ErrInvalidCredentials
	}
	if errors.Is(err, api.ErrVersionConflict) {
		return api.ErrVersionConflict
	}
	if errors.Is(err, api.ErrResourceNotFound) {
		return api.ErrResourceNotFound
	}
	if errors.Is(err, idempotency.ErrKeyConflict) {
		return api.ErrIdempotencyConflict
	}
	if errors.Is(err, idempotency.ErrInProgress) || errors.Is(err, eventpostgres.ErrVersionConflict) || errors.Is(err, eventpostgres.ErrCommandConflict) || errors.Is(err, api.ErrStateConflict) {
		return api.ErrStateConflict
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return api.ErrStateConflict
	}
	return api.ErrDependencyUnavailable
}
