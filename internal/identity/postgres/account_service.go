package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
)

type accountMutationPrepared struct {
	ResourceID, SecurityEventID, EventID, PublishOutboxID, PublishCommandID string
	WorkOutboxID, WorkCommandID                                             string
	EventPayload, WorkCommand, SensitivePayload, Response                   payload.Manifest
}

type storedAccountMutation struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (service AuthService) CreateAccountExport(ctx context.Context, command api.AccountExportCreateCommand) (api.AccountMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.AccountMutationResult{}, err
	}
	if len(command.Scope) == 0 || len(command.Scope) > 7 || (command.Format != "json" && command.Format != "zip") {
		return api.AccountMutationResult{}, api.ErrValidation
	}
	allowed := map[string]struct{}{"account": {}, "missions": {}, "evidence": {}, "projects": {}, "conversations": {}, "memory": {}, "audit": {}}
	canonicalScope := append([]string(nil), command.Scope...)
	sort.Strings(canonicalScope)
	for index, value := range canonicalScope {
		if _, ok := allowed[value]; !ok || (index > 0 && canonicalScope[index-1] == value) {
			return api.AccountMutationResult{}, api.ErrValidation
		}
	}
	command.Scope = canonicalScope
	recordID, requestHash, err := service.authenticatedIdempotency("account.export.create", command.UserID, command.IdempotencyKey, struct {
		Scope  []string `json:"scope"`
		Format string   `json:"format"`
	}{command.Scope, command.Format})
	if err != nil {
		return api.AccountMutationResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "account.export.create"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadAccountMutationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeRecentSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		return service.accountMutationReplayOr(ctx, input, descriptor, err)
	}
	now := service.now()
	prepared, err := service.prepareAccountExport(ctx, command, recordID, now)
	if err != nil {
		return api.AccountMutationResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, lockErr := tx.Exec(ctx, `SELECT id FROM identity.users WHERE id=$1 FOR UPDATE`, command.UserID); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if authErr := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); authErr != nil {
			return idempotency.Response{}, authErr
		}
		if authErr := service.requireRecentReauthentication(ctx, tx, command.AuthenticatedRequestMetadata, now); authErr != nil {
			return idempotency.Response{}, authErr
		}
		scope, _ := json.Marshal(map[string]any{"categories": command.Scope, "format": command.Format})
		if _, insertErr := tx.Exec(ctx, `INSERT INTO product.data_export_requests (id,tenant_id,user_id,status,scope) VALUES ($1,$2,$3,'requested',$4)`, prepared.ResourceID, command.TenantID, command.UserID, scope); insertErr != nil {
			return idempotency.Response{}, insertErr
		}
		if _, auditErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'account_export_requested',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, command.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"reauthenticated":true}`), now); auditErr != nil {
			return idempotency.Response{}, auditErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		_, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "AccountExportRequested", SchemaVersion: 1, AggregateKind: "data_export_request", AggregateID: prepared.ResourceID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.WorkOutboxID, CommandID: prepared.WorkCommandID, CommandType: "account.export.prepare", PayloadRef: prepared.WorkCommand.Ref, PayloadHash: prepared.WorkCommand.Hash}}})
		if appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return service.accountMutationReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readAccountMutation(ctx, descriptor, response)
}

func (service AuthService) prepareAccountExport(ctx context.Context, command api.AccountExportCreateCommand, recordID string, now time.Time) (accountMutationPrepared, error) {
	prepared, err := service.newAccountPrepared(true)
	if err != nil {
		return accountMutationPrepared{}, err
	}
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.ResourceID, "subject_version": 1, "request_id": command.RequestID})
	if err == nil {
		prepared.WorkCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: prepared.WorkCommandID, Class: "account-export-command", ContentType: "application/json"}, map[string]any{"request_id": prepared.ResourceID, "user_id": command.UserID, "scope": command.Scope, "format": command.Format})
	}
	if err == nil {
		prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedAccountMutation{prepared.ResourceID, 1, "requested", now})
	}
	if err != nil {
		return accountMutationPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) CreateAccountErasure(ctx context.Context, command api.AccountErasureCreateCommand) (api.AccountMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.AccountMutationResult{}, err
	}
	if command.Reason != nil && utf8.RuneCountInString(*command.Reason) > 1000 {
		return api.AccountMutationResult{}, api.ErrValidation
	}
	recordID, err := service.idempotencyRecordID("account.erasure.create", command.UserID, command.IdempotencyKey)
	if err != nil {
		return api.AccountMutationResult{}, api.ErrIdempotencyConflict
	}
	canonical, _ := json.Marshal(struct {
		Reason *string `json:"reason"`
	}{command.Reason})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.AccountMutationResult{}, api.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "account.erasure.create"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadAccountMutationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeRecentSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		return service.accountMutationReplayOr(ctx, input, descriptor, err)
	}
	now := service.now()
	prepared, err := service.prepareAccountErasure(ctx, command, recordID, now)
	if err != nil {
		return api.AccountMutationResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, lockErr := tx.Exec(ctx, `SELECT id FROM identity.users WHERE id=$1 FOR UPDATE`, command.UserID); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if authErr := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); authErr != nil {
			return idempotency.Response{}, authErr
		}
		if authErr := service.requireRecentReauthentication(ctx, tx, command.AuthenticatedRequestMetadata, now); authErr != nil {
			return idempotency.Response{}, authErr
		}
		var activeID string
		activeErr := tx.QueryRow(ctx, `SELECT id::text FROM identity.account_erasure_requests WHERE tenant_id=$1 AND user_id=$2 AND status IN ('requested','processing') LIMIT 1 FOR UPDATE`, command.TenantID, command.UserID).Scan(&activeID)
		if activeErr == nil {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if !errors.Is(activeErr, pgx.ErrNoRows) {
			return idempotency.Response{}, activeErr
		}
		scheduledFor := now.Add(service.ErasureGracePeriod)
		if _, insertErr := tx.Exec(ctx, `INSERT INTO identity.account_erasure_requests (id,tenant_id,user_id,status,requested_at,scheduled_for) VALUES ($1,$2,$3,'requested',$4,$5)`, prepared.ResourceID, command.TenantID, command.UserID, now, scheduledFor); insertErr != nil {
			return idempotency.Response{}, insertErr
		}
		if _, updateErr := tx.Exec(ctx, `UPDATE identity.users SET deletion_requested_at=$1,updated_at=$1 WHERE id=$2`, now, command.UserID); updateErr != nil {
			return idempotency.Response{}, updateErr
		}
		details, _ := json.Marshal(map[string]any{"reason_ref": prepared.SensitivePayload.Ref, "reason_hash": prepared.SensitivePayload.Hash, "scheduled_for": scheduledFor})
		if _, auditErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'account_erasure_requested',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, command.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, now); auditErr != nil {
			return idempotency.Response{}, auditErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		_, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "AccountErasureRequested", SchemaVersion: 1, AggregateKind: "account_erasure_request", AggregateID: prepared.ResourceID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.WorkOutboxID, CommandID: prepared.WorkCommandID, CommandType: "account.erasure.schedule", PayloadRef: prepared.WorkCommand.Ref, PayloadHash: prepared.WorkCommand.Hash, AvailableAt: scheduledFor}}})
		if appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return service.accountMutationReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readAccountMutation(ctx, descriptor, response)
}

func (service AuthService) prepareAccountErasure(ctx context.Context, command api.AccountErasureCreateCommand, recordID string, now time.Time) (accountMutationPrepared, error) {
	prepared, err := service.newAccountPrepared(true)
	if err != nil {
		return accountMutationPrepared{}, err
	}
	scheduledFor := now.Add(service.ErasureGracePeriod)
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": prepared.ResourceID, "subject_version": 1, "request_id": command.RequestID})
	if err == nil {
		prepared.WorkCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: prepared.WorkCommandID, Class: "account-erasure-command", ContentType: "application/json"}, map[string]any{"request_id": prepared.ResourceID, "user_id": command.UserID, "scheduled_for": scheduledFor})
	}
	if err == nil {
		prepared.SensitivePayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: prepared.ResourceID, Class: "account-erasure-reason", ContentType: "application/json"}, map[string]any{"reason": command.Reason})
	}
	if err == nil {
		prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedAccountMutation{prepared.ResourceID, 1, "requested", now})
	}
	if err != nil {
		return accountMutationPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) CancelAccountErasure(ctx context.Context, command api.AccountErasureCancelCommand) (api.AccountMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.AccountMutationResult{}, err
	}
	if command.ErasureID == "" || command.ExpectedVersion == 0 {
		return api.AccountMutationResult{}, api.ErrValidation
	}
	recordID, requestHash, err := service.authenticatedIdempotency("account.erasure.cancel", command.UserID, command.IdempotencyKey, struct {
		ID      string `json:"id"`
		Version uint64 `json:"expected_version"`
	}{command.ErasureID, command.ExpectedVersion})
	if err != nil {
		return api.AccountMutationResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "account.erasure.cancel"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadAccountMutationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeRecentSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		return service.accountMutationReplayOr(ctx, input, descriptor, err)
	}
	now := service.now()
	prepared, err := service.prepareAccountCancellation(ctx, command, recordID, now)
	if err != nil {
		return api.AccountMutationResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, lockErr := tx.Exec(ctx, `SELECT id FROM identity.users WHERE id=$1 FOR UPDATE`, command.UserID); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if authErr := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); authErr != nil {
			return idempotency.Response{}, authErr
		}
		if authErr := service.requireRecentReauthentication(ctx, tx, command.AuthenticatedRequestMetadata, now); authErr != nil {
			return idempotency.Response{}, authErr
		}
		var version uint64
		var status string
		var scheduledFor time.Time
		err := tx.QueryRow(ctx, `SELECT version,status,scheduled_for FROM identity.account_erasure_requests WHERE id=$1 AND tenant_id=$2 AND user_id=$3 FOR UPDATE`, command.ErasureID, command.TenantID, command.UserID).Scan(&version, &status, &scheduledFor)
		if errors.Is(err, pgx.ErrNoRows) {
			return idempotency.Response{}, api.ErrResourceNotFound
		}
		if err != nil {
			return idempotency.Response{}, err
		}
		if version != command.ExpectedVersion {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if status != "requested" || !scheduledFor.After(now) {
			return idempotency.Response{}, api.ErrStateConflict
		}
		var nextVersion uint64
		if err = tx.QueryRow(ctx, `UPDATE identity.account_erasure_requests SET status='cancelled',cancelled_at=$1,version=version+1,updated_at=$1 WHERE id=$2 AND version=$3 RETURNING version`, now, command.ErasureID, command.ExpectedVersion).Scan(&nextVersion); err != nil {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.users SET deletion_requested_at=NULL,updated_at=$1 WHERE id=$2`, now, command.UserID); err != nil {
			return idempotency.Response{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'account_erasure_cancelled',$4,$5,$6,'{}',$7)`, prepared.SecurityEventID, command.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, now); err != nil {
			return idempotency.Response{}, err
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "AccountErasureCancelled", SchemaVersion: 1, AggregateKind: "account_erasure_request", AggregateID: command.ErasureID, AggregateVersion: nextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}}})
		if err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: nextVersion}, nil
	})
	if err != nil {
		return service.accountMutationReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readAccountMutation(ctx, descriptor, response)
}

func (service AuthService) prepareAccountCancellation(ctx context.Context, command api.AccountErasureCancelCommand, recordID string, now time.Time) (accountMutationPrepared, error) {
	prepared, err := service.newAccountPrepared(false)
	if err != nil {
		return accountMutationPrepared{}, err
	}
	prepared.ResourceID = command.ErasureID
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": command.ErasureID, "subject_version": command.ExpectedVersion + 1, "previous_state": "requested", "new_state": "cancelled", "reason_code": "user_cancelled"})
	if err == nil {
		prepared.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedAccountMutation{command.ErasureID, command.ExpectedVersion + 1, "cancelled", now})
	}
	if err != nil {
		return accountMutationPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) newAccountPrepared(withWork bool) (accountMutationPrepared, error) {
	var prepared accountMutationPrepared
	targets := []*string{&prepared.ResourceID, &prepared.SecurityEventID, &prepared.EventID, &prepared.PublishOutboxID, &prepared.PublishCommandID}
	if withWork {
		targets = append(targets, &prepared.WorkOutboxID, &prepared.WorkCommandID)
	}
	for _, target := range targets {
		value, err := service.newID()
		if err != nil {
			return accountMutationPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	return prepared, nil
}

func (service AuthService) readAccountMutation(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.AccountMutationResult, error) {
	var stored storedAccountMutation
	if err := service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return api.AccountMutationResult{}, api.ErrDependencyUnavailable
	}
	return api.AccountMutationResult{ID: stored.ID, Version: stored.Version, Status: stored.Status, UpdatedAt: stored.UpdatedAt}, nil
}

func (service AuthService) loadAccountMutationReplay(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor) (api.AccountMutationResult, bool, error) {
	response, found, err := service.idempotencyExecutor().LoadCompleted(ctx, input)
	if err != nil {
		return api.AccountMutationResult{}, false, mapIdentityError(err)
	}
	if !found {
		return api.AccountMutationResult{}, false, nil
	}
	result, err := service.readAccountMutation(ctx, descriptor, response)
	return result, true, err
}

func (service AuthService) accountMutationReplayOr(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor, fallback error) (api.AccountMutationResult, error) {
	if result, found, err := service.loadAccountMutationReplay(ctx, input, descriptor); err != nil || found {
		return result, err
	}
	return api.AccountMutationResult{}, fallback
}

var _ api.AccountService = AuthService{}
