package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
)

func (service AuthService) GetAccount(ctx context.Context, query api.AccountQuery) (api.AccountResource, error) {
	if err := service.validateSessionRequest(query.AuthenticatedRequestMetadata); err != nil {
		return api.AccountResource{}, err
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return api.AccountResource{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.now()
	if err = service.authorizeSession(ctx, tx, query.AuthenticatedRequestMetadata, now, false); err != nil {
		return api.AccountResource{}, err
	}
	var result api.AccountResource
	err = tx.QueryRow(ctx, `SELECT id::text,normalized_email,email_verified_at IS NOT NULL,locale,timezone,display_name,version,updated_at FROM identity.users WHERE id=$1 AND status='active'`, query.UserID).Scan(&result.UserID, &result.NormalizedEmail, &result.EmailVerified, &result.Locale, &result.Timezone, &result.DisplayName, &result.Version, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.AccountResource{}, api.ErrResourceNotFound
	}
	if err != nil {
		return api.AccountResource{}, api.ErrDependencyUnavailable
	}
	auditID, err := service.newID()
	if err != nil {
		return api.AccountResource{}, api.ErrDependencyUnavailable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events(id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES($1,$2,$3,$3,'account_read',$4,$5,$6,'{}',$7)`, auditID, query.TenantID, query.UserID, query.RequestID, query.ClientIPHash, query.UserAgentHash, now); err != nil {
		return api.AccountResource{}, api.ErrDependencyUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return api.AccountResource{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

func (service AuthService) UpdateAccount(ctx context.Context, command api.AccountUpdateCommand) (api.AccountMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.AccountMutationResult{}, err
	}
	if command.ExpectedVersion < 1 || !validStoredAccountChanges(command.Changes) {
		return api.AccountMutationResult{}, api.ErrValidation
	}
	recordID, requestHash, err := service.authenticatedIdempotency("account.update", command.UserID, command.IdempotencyKey, struct {
		ExpectedVersion uint64             `json:"expected_version"`
		Changes         api.AccountChanges `json:"changes"`
	}{command.ExpectedVersion, command.Changes})
	if err != nil {
		return api.AccountMutationResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "account.update"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadAccountMutationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		return service.accountMutationReplayOr(ctx, input, descriptor, err)
	}
	now := service.now()
	securityEventID, eventID, outboxID, publishID, err := service.fourIDs()
	if err != nil {
		return api.AccountMutationResult{}, api.ErrDependencyUnavailable
	}
	changedFields := accountChangedFields(command.Changes)
	eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": command.UserID, "subject_version": command.ExpectedVersion + 1, "previous_state": "active", "new_state": "active", "reason_code": "profile_updated", "changed_fields": changedFields})
	if err != nil {
		return api.AccountMutationResult{}, api.ErrDependencyUnavailable
	}
	responsePayload, err := service.putJSON(ctx, descriptor, storedAccountMutation{ID: command.UserID, Version: command.ExpectedVersion + 1, Status: "updated", UpdatedAt: now})
	if err != nil {
		return api.AccountMutationResult{}, api.ErrDependencyUnavailable
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if authErr := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); authErr != nil {
			return idempotency.Response{}, authErr
		}
		var version uint64
		var locale, timezone string
		var displayName *string
		if lockErr := tx.QueryRow(ctx, `SELECT version,locale,timezone,display_name FROM identity.users WHERE id=$1 AND status='active' FOR UPDATE`, command.UserID).Scan(&version, &locale, &timezone, &displayName); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if version != command.ExpectedVersion {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if command.Changes.Locale != nil {
			locale = *command.Changes.Locale
		}
		if command.Changes.Timezone != nil {
			timezone = *command.Changes.Timezone
		}
		if command.Changes.DisplayNameSet {
			displayName = command.Changes.DisplayName
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE identity.users SET locale=$1,timezone=$2,display_name=$3,version=version+1,updated_at=$4 WHERE id=$5 AND version=$6`, locale, timezone, displayName, now, command.UserID, command.ExpectedVersion)
		if updateErr != nil {
			return idempotency.Response{}, updateErr
		}
		if tag.RowsAffected() != 1 {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		details, _ := json.Marshal(map[string]any{"changed_fields": changedFields})
		if _, auditErr := tx.Exec(ctx, `INSERT INTO identity.security_events(id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES($1,$2,$3,$3,'account_updated',$4,$5,$6,$7,$8)`, securityEventID, command.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, now); auditErr != nil {
			return idempotency.Response{}, auditErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		if _, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "AccountProfileUpdated", SchemaVersion: 1, AggregateKind: "user", AggregateID: command.UserID, AggregateVersion: command.ExpectedVersion + 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: command.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}}); appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: responsePayload.Ref, Hash: responsePayload.Hash, ResourceVersion: command.ExpectedVersion + 1}, nil
	})
	if err != nil {
		return service.accountMutationReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readAccountMutation(ctx, descriptor, response)
}

func validStoredAccountChanges(changes api.AccountChanges) bool {
	if changes.Locale == nil && changes.Timezone == nil && !changes.DisplayNameSet {
		return false
	}
	if changes.Locale != nil && *changes.Locale != "en" && *changes.Locale != "zh-CN" {
		return false
	}
	if changes.Timezone != nil {
		if _, err := time.LoadLocation(*changes.Timezone); err != nil || len(*changes.Timezone) > 128 || strings.Contains(*changes.Timezone, "..") {
			return false
		}
	}
	if changes.DisplayNameSet && changes.DisplayName != nil {
		value := *changes.DisplayName
		if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 200 || strings.ContainsFunc(value, unicode.IsControl) {
			return false
		}
	}
	return true
}

func accountChangedFields(changes api.AccountChanges) []string {
	fields := []string{}
	if changes.Locale != nil {
		fields = append(fields, "locale")
	}
	if changes.Timezone != nil {
		fields = append(fields, "timezone")
	}
	if changes.DisplayNameSet {
		fields = append(fields, "display_name")
	}
	return fields
}

func (service AuthService) fourIDs() (string, string, string, string, error) {
	values := make([]string, 4)
	for index := range values {
		value, err := service.newID()
		if err != nil {
			return "", "", "", "", err
		}
		values[index] = value
	}
	return values[0], values[1], values[2], values[3], nil
}
