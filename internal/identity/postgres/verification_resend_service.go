package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
)

const verificationResendCooldown = 15 * time.Minute

type verificationResendAccount struct {
	UserID, TenantID, Email, Locale, Status string
	Version                                 uint64
	VerifiedAt                              *time.Time
}

type verificationResendPrepared struct {
	VerificationID, SecurityEventID, EventID                       string
	PublishOutboxID, PublishCommandID, MailOutboxID, MailCommandID string
	TokenDigest                                                    []byte
	ExpiresAt                                                      time.Time
	EventPayload, MailCommand                                      payload.Manifest
}

func (service AuthService) ResendVerification(ctx context.Context, command api.ResendVerificationCommand) (api.ResendVerificationResult, error) {
	if err := service.validatePasswordPublic(command.RequestMetadata); err != nil {
		return api.ResendVerificationResult{}, err
	}
	if command.NormalizedEmail == "" {
		return api.ResendVerificationResult{}, api.ErrValidation
	}
	publicPrincipal, recordID, requestHash, err := service.publicIdempotency("auth.resend_verification", command.NormalizedEmail, command.IdempotencyKey, struct {
		Email string `json:"email"`
	}{Email: command.NormalizedEmail})
	if err != nil {
		return api.ResendVerificationResult{}, api.ErrIdempotencyConflict
	}
	now := service.now()
	result := api.ResendVerificationResult{Status: "accepted", NextAllowedAt: now.Add(verificationResendCooldown)}
	responseDescriptor := payload.Descriptor{TenantID: service.PublicTenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	responseManifest, err := service.putJSON(ctx, responseDescriptor, result)
	if err != nil {
		return api.ResendVerificationResult{}, api.ErrDependencyUnavailable
	}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: service.PublicTenantID, UserID: publicPrincipal, OperationID: "auth.resend_verification"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if response, found, replayErr := service.idempotencyExecutor().LoadCompleted(ctx, input); replayErr != nil {
		return api.ResendVerificationResult{}, mapIdentityError(replayErr)
	} else if found {
		return service.readVerificationResendResponse(ctx, responseDescriptor, response)
	}

	account, found, err := service.lookupVerificationResendAccount(ctx, command.NormalizedEmail)
	if err != nil {
		return api.ResendVerificationResult{}, api.ErrDependencyUnavailable
	}
	if !found || account.Status != "pending_verification" || account.VerifiedAt != nil {
		response, _, executeErr := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
			eventID, idErr := service.newID()
			if idErr != nil {
				return idempotency.Response{}, idErr
			}
			details, _ := json.Marshal(map[string]string{"email_principal_id": publicPrincipal})
			if _, insertErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,'email_verification_resend_requested',$3,$4,$5,$6,$7)`, eventID, service.PublicTenantID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, now); insertErr != nil {
				return idempotency.Response{}, insertErr
			}
			return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: 1}, nil
		})
		if executeErr != nil {
			return api.ResendVerificationResult{}, mapIdentityError(executeErr)
		}
		return service.readVerificationResendResponse(ctx, responseDescriptor, response)
	}

	prepared, err := service.prepareVerificationResend(ctx, account, command, now)
	if err != nil {
		return api.ResendVerificationResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		var version uint64
		var status string
		var verifiedAt *time.Time
		lockErr := tx.QueryRow(ctx, `SELECT version,status,email_verified_at FROM identity.users WHERE id=$1 FOR UPDATE`, account.UserID).Scan(&version, &status, &verifiedAt)
		if errors.Is(lockErr, pgx.ErrNoRows) || lockErr == nil && (version != account.Version || status != "pending_verification" || verifiedAt != nil) {
			return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: 1}, nil
		}
		if lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, account.TenantID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.email_verifications SET revoked_at=$1 WHERE user_id=$2 AND used_at IS NULL AND revoked_at IS NULL`, now, account.UserID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.email_verifications (id,user_id,token_hash,email,expires_at) VALUES ($1,$2,$3,$4,$5)`, prepared.VerificationID, account.UserID, prepared.TokenDigest, account.Email, prepared.ExpiresAt); err != nil {
			return idempotency.Response{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'email_verification_resend_requested',$4,$5,$6,'{}',$7)`, prepared.SecurityEventID, account.TenantID, account.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, now); err != nil {
			return idempotency.Response{}, err
		}
		actor := json.RawMessage(`{"kind":"system","id":"identity-service"}`)
		_, err := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: account.TenantID, UserID: account.UserID, EventType: "EmailVerificationRequested", SchemaVersion: 1, AggregateKind: "email_verification", AggregateID: prepared.VerificationID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.MailOutboxID, CommandID: prepared.MailCommandID, CommandType: "identity.email.verify", PayloadRef: prepared.MailCommand.Ref, PayloadHash: prepared.MailCommand.Hash}}})
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return api.ResendVerificationResult{}, mapIdentityError(err)
	}
	return service.readVerificationResendResponse(ctx, responseDescriptor, response)
}

func (service AuthService) lookupVerificationResendAccount(ctx context.Context, normalizedEmail string) (verificationResendAccount, bool, error) {
	var value verificationResendAccount
	err := service.Pool.QueryRow(ctx, `WITH personal AS (SELECT owner_user_id,(array_agg(id ORDER BY created_at,id))[1]::text AS tenant_id,count(*) AS tenant_count FROM identity.tenants WHERE kind='personal' GROUP BY owner_user_id) SELECT u.id::text,p.tenant_id,u.normalized_email,u.locale,u.status,u.version,u.email_verified_at FROM identity.users u JOIN personal p ON p.owner_user_id=u.id AND p.tenant_count=1 WHERE u.normalized_email=$1`, normalizedEmail).Scan(&value.UserID, &value.TenantID, &value.Email, &value.Locale, &value.Status, &value.Version, &value.VerifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return verificationResendAccount{}, false, nil
	}
	if err != nil {
		return verificationResendAccount{}, false, err
	}
	return value, true, nil
}

func (service AuthService) prepareVerificationResend(ctx context.Context, account verificationResendAccount, command api.ResendVerificationCommand, now time.Time) (verificationResendPrepared, error) {
	prepared := verificationResendPrepared{ExpiresAt: now.Add(service.VerificationTTL)}
	for _, target := range []*string{&prepared.VerificationID, &prepared.SecurityEventID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID, &prepared.MailOutboxID, &prepared.MailCommandID} {
		value, err := service.newID()
		if err != nil {
			return verificationResendPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	manager := service.VerificationTokens
	if manager.Random == nil {
		manager.Random = service.random()
	}
	token, err := manager.Issue()
	if err != nil {
		return verificationResendPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.TokenDigest = append([]byte(nil), token.Digest[:]...)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: account.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.VerificationID, "subject_version": 1, "request_id": command.RequestID})
	if err != nil {
		return verificationResendPrepared{}, api.ErrDependencyUnavailable
	}
	prepared.MailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: account.TenantID, ObjectID: prepared.MailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "verify-email-v1", "locale": account.Locale, "recipient": account.Email, "token": token.Raw, "expires_at": prepared.ExpiresAt})
	if err != nil {
		return verificationResendPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) readVerificationResendResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.ResendVerificationResult, error) {
	var result api.ResendVerificationResult
	if err := service.readResponse(ctx, descriptor, response, &result); err != nil {
		return api.ResendVerificationResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

var _ api.Service = AuthService{}
