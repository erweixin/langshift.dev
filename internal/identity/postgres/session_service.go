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
	"github.com/langshift/lites/internal/platform/cursor"
)

const maximumSessionsPage = 100

type sessionRow struct {
	ID             string
	ActiveTenantID string
	Version        uint64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSeenAt     time.Time
	ExpiresAt      time.Time
	RevokedAt      *time.Time
	DeviceLabel    *string
}

type sessionCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type sessionRevocationPrepared struct {
	Session      sessionRow
	EventID      string
	OutboxID     string
	CommandID    string
	EventPayload payload.Manifest
	NextVersion  uint64
}

func (service AuthService) ListSessions(ctx context.Context, query api.SessionsQuery) (api.SessionsPage, error) {
	if err := service.validateSessionRequest(query.AuthenticatedRequestMetadata); err != nil {
		return api.SessionsPage{}, err
	}
	limit := query.Limit
	if limit < 1 || limit > maximumSessionsPage {
		return api.SessionsPage{}, api.ErrValidation
	}
	var after sessionCursor
	if query.Cursor != "" {
		if err := (cursor.Codec{Key: service.CursorKey}).Decode(query.Cursor, service.sessionCursorScope(query.UserID), &after); err != nil || after.ID == "" || after.CreatedAt.IsZero() {
			return api.SessionsPage{}, api.ErrValidation
		}
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return api.SessionsPage{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.authorizeSession(ctx, tx, query.AuthenticatedRequestMetadata, service.now(), false); err != nil {
		return api.SessionsPage{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,active_tenant_id::text,version,created_at,updated_at,last_seen_at,expires_at,revoked_at,device_label FROM identity.sessions WHERE user_id=$1 AND revoked_at IS NULL AND expires_at>$2 AND ($3::timestamptz IS NULL OR (created_at,id)<($3,$4::uuid)) ORDER BY created_at DESC,id DESC LIMIT $5`, query.UserID, service.now(), nullableTime(after.CreatedAt), nullableString(after.ID), limit+1)
	if err != nil {
		return api.SessionsPage{}, api.ErrDependencyUnavailable
	}
	defer rows.Close()
	items := make([]api.SessionItem, 0, limit+1)
	for rows.Next() {
		var row sessionRow
		if err = rows.Scan(&row.ID, &row.ActiveTenantID, &row.Version, &row.CreatedAt, &row.UpdatedAt, &row.LastSeenAt, &row.ExpiresAt, &row.RevokedAt, &row.DeviceLabel); err != nil {
			return api.SessionsPage{}, api.ErrDependencyUnavailable
		}
		items = append(items, api.SessionItem{ID: row.ID, ActiveTenantID: row.ActiveTenantID, Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, LastSeenAt: row.LastSeenAt, ExpiresAt: row.ExpiresAt, RevokedAt: row.RevokedAt, DeviceLabel: row.DeviceLabel, Current: row.ID == query.SessionID})
	}
	if err = rows.Err(); err != nil {
		return api.SessionsPage{}, api.ErrDependencyUnavailable
	}
	var next *string
	if len(items) > limit {
		last := items[limit-1]
		token, encodeErr := (cursor.Codec{Key: service.CursorKey}).Encode(service.sessionCursorScope(query.UserID), sessionCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		if encodeErr != nil {
			return api.SessionsPage{}, api.ErrDependencyUnavailable
		}
		next = &token
		items = items[:limit]
	}
	if err = tx.Commit(ctx); err != nil {
		return api.SessionsPage{}, api.ErrDependencyUnavailable
	}
	return api.SessionsPage{Items: items, NextCursor: next}, nil
}

func (service AuthService) RevokeSession(ctx context.Context, command api.RevokeSessionCommand) (api.SessionMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.SessionMutationResult{}, err
	}
	if command.TargetSessionID == "" || command.ExpectedVersion == 0 || command.ReasonCode == "" {
		return api.SessionMutationResult{}, api.ErrValidation
	}
	recordID, requestHash, err := service.authenticatedIdempotency("auth.sessions.revoke", command.UserID, command.IdempotencyKey, struct {
		SessionID       string `json:"session_id"`
		ExpectedVersion uint64 `json:"expected_version"`
		ReasonCode      string `json:"reason_code"`
	}{command.TargetSessionID, command.ExpectedVersion, command.ReasonCode})
	if err != nil {
		return api.SessionMutationResult{}, api.ErrIdempotencyConflict
	}
	responseDescriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	idempotencyInput := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "auth.sessions.revoke"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadSessionMutationReplay(ctx, idempotencyInput, responseDescriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeSession(ctx, command.AuthenticatedRequestMetadata); err != nil {
		if result, found, replayErr := service.loadSessionMutationReplay(ctx, idempotencyInput, responseDescriptor); replayErr != nil || found {
			return result, replayErr
		}
		return api.SessionMutationResult{}, err
	}
	target, err := service.lookupOwnedSession(ctx, command.UserID, command.TargetSessionID)
	if err != nil {
		return service.sessionMutationReplayOr(ctx, idempotencyInput, responseDescriptor, err)
	}
	if target.Version != command.ExpectedVersion {
		return service.sessionMutationReplayOr(ctx, idempotencyInput, responseDescriptor, api.ErrVersionConflict)
	}
	if target.RevokedAt != nil {
		return service.sessionMutationReplayOr(ctx, idempotencyInput, responseDescriptor, api.ErrStateConflict)
	}
	now := service.now()
	prepared, err := service.prepareSessionRevocation(ctx, target, command.ReasonCode)
	if err != nil {
		return api.SessionMutationResult{}, err
	}
	responseManifest, err := service.putJSON(ctx, responseDescriptor, map[string]any{"id": target.ID, "version": prepared.NextVersion, "status": "revoked", "updated_at": now})
	if err != nil {
		return api.SessionMutationResult{}, api.ErrDependencyUnavailable
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if err := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); err != nil {
			return idempotency.Response{}, err
		}
		if command.TargetSessionID != command.SessionID {
			current, lockErr := service.lockOwnedSession(ctx, tx, command.UserID, command.TargetSessionID)
			if lockErr != nil {
				return idempotency.Response{}, lockErr
			}
			if current.Version != command.ExpectedVersion || current.RevokedAt != nil {
				return idempotency.Response{}, api.ErrVersionConflict
			}
		}
		if err := service.commitSessionRevocations(ctx, tx, command.AuthenticatedRequestMetadata, []sessionRevocationPrepared{prepared}, command.ReasonCode, now); err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: prepared.NextVersion}, nil
	})
	if err != nil {
		return api.SessionMutationResult{}, mapIdentityError(err)
	}
	var result api.SessionMutationResult
	if err = service.readResponse(ctx, responseDescriptor, response, &result); err != nil {
		return api.SessionMutationResult{}, api.ErrDependencyUnavailable
	}
	return result, nil
}

func (service AuthService) Logout(ctx context.Context, command api.LogoutCommand) (api.LogoutResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.LogoutResult{}, err
	}
	reason := "logout"
	if command.AllDevices {
		reason = "logout_all_devices"
	}
	result, err := service.revokeSessionBatch(ctx, command.AuthenticatedRequestMetadata, "auth.logout", false, reason, func(row sessionRow) bool {
		return command.AllDevices || row.ID == command.SessionID
	})
	if err != nil {
		return api.LogoutResult{}, err
	}
	return api.LogoutResult{RevokedSessionCount: result.RevokedCount}, nil
}

func (service AuthService) RevokeOtherSessions(ctx context.Context, command api.RevokeOtherSessionsCommand) (api.SessionMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.SessionMutationResult{}, err
	}
	result, err := service.revokeSessionBatch(ctx, command.AuthenticatedRequestMetadata, "auth.sessions.revoke_others", true, "revoke_others", func(row sessionRow) bool {
		return row.ID != command.SessionID
	})
	if err != nil {
		return api.SessionMutationResult{}, err
	}
	return result.Current, nil
}

type sessionBatchResult struct {
	Current      api.SessionMutationResult `json:"current"`
	RevokedCount int                       `json:"revoked_count"`
}

func (service AuthService) revokeSessionBatch(ctx context.Context, metadata api.AuthenticatedRequestMetadata, operation string, preserveCurrent bool, reason string, include func(sessionRow) bool) (sessionBatchResult, error) {
	recordID, requestHash, err := service.authenticatedIdempotency(operation, metadata.UserID, metadata.IdempotencyKey, struct {
		CurrentSessionID string `json:"current_session_id"`
		Reason           string `json:"reason"`
	}{metadata.SessionID, reason})
	if err != nil {
		return sessionBatchResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: metadata.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	idempotencyInput := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: metadata.TenantID, UserID: metadata.UserID, OperationID: operation}, RawKey: metadata.IdempotencyKey, RequestHash: requestHash, RequestID: metadata.RequestID}
	if result, found, replayErr := service.loadSessionBatchReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err := service.preauthorizeSession(ctx, metadata); err != nil {
		if result, found, replayErr := service.loadSessionBatchReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
			return result, replayErr
		}
		return sessionBatchResult{}, err
	}
	rows, err := service.lookupActiveOwnedSessions(ctx, metadata.UserID)
	if err != nil {
		return sessionBatchResult{}, err
	}
	now := service.now()
	prepared := make([]sessionRevocationPrepared, 0, len(rows))
	var current sessionRow
	for _, row := range rows {
		if row.ID == metadata.SessionID {
			current = row
		}
		if !include(row) {
			continue
		}
		value, prepareErr := service.prepareSessionRevocation(ctx, row, reason)
		if prepareErr != nil {
			return sessionBatchResult{}, prepareErr
		}
		prepared = append(prepared, value)
	}
	if current.ID == "" || (!preserveCurrent && len(prepared) == 0) {
		if replayed, found, replayErr := service.loadSessionBatchReplay(ctx, idempotencyInput, descriptor); replayErr != nil || found {
			return replayed, replayErr
		}
		return sessionBatchResult{}, api.ErrInvalidCredentials
	}
	result := sessionBatchResult{Current: api.SessionMutationResult{ID: current.ID, Version: current.Version, Status: "active", UpdatedAt: current.UpdatedAt}, RevokedCount: len(prepared)}
	if !preserveCurrent {
		result.Current.Version = current.Version + 1
		result.Current.Status = "revoked"
		result.Current.UpdatedAt = now
	}
	manifest, err := service.putJSON(ctx, descriptor, result)
	if err != nil {
		return sessionBatchResult{}, api.ErrDependencyUnavailable
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, idempotencyInput, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, lockErr := tx.Exec(ctx, `SELECT id FROM identity.users WHERE id=$1 FOR UPDATE`, metadata.UserID); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if err := service.authorizeSession(ctx, tx, metadata, now, true); err != nil {
			return idempotency.Response{}, err
		}
		locked, lockErr := service.lockActiveOwnedSessions(ctx, tx, metadata.UserID)
		if lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		actual := make([]sessionRow, 0, len(locked))
		currentUnchanged := !preserveCurrent
		for _, row := range locked {
			if preserveCurrent && row.ID == current.ID && row.Version == current.Version {
				currentUnchanged = true
			}
			if include(row) {
				actual = append(actual, row)
			}
		}
		if !currentUnchanged || !sameSessionSnapshot(prepared, actual) {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if err := service.commitSessionRevocations(ctx, tx, metadata, prepared, reason, now); err != nil {
			return idempotency.Response{}, err
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.Current.Version}, nil
	})
	if err != nil {
		return sessionBatchResult{}, mapIdentityError(err)
	}
	var stored sessionBatchResult
	if err = service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return sessionBatchResult{}, api.ErrDependencyUnavailable
	}
	return stored, nil
}

func (service AuthService) loadSessionMutationReplay(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor) (api.SessionMutationResult, bool, error) {
	response, found, err := service.idempotencyExecutor().LoadCompleted(ctx, input)
	if err != nil {
		return api.SessionMutationResult{}, false, mapIdentityError(err)
	}
	if !found {
		return api.SessionMutationResult{}, false, nil
	}
	var result api.SessionMutationResult
	if err = service.readResponse(ctx, descriptor, response, &result); err != nil {
		return api.SessionMutationResult{}, false, api.ErrDependencyUnavailable
	}
	return result, true, nil
}

func (service AuthService) sessionMutationReplayOr(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor, fallback error) (api.SessionMutationResult, error) {
	result, found, err := service.loadSessionMutationReplay(ctx, input, descriptor)
	if err != nil {
		return api.SessionMutationResult{}, err
	}
	if found {
		return result, nil
	}
	return api.SessionMutationResult{}, fallback
}

func (service AuthService) loadSessionBatchReplay(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor) (sessionBatchResult, bool, error) {
	response, found, err := service.idempotencyExecutor().LoadCompleted(ctx, input)
	if err != nil {
		return sessionBatchResult{}, false, mapIdentityError(err)
	}
	if !found {
		return sessionBatchResult{}, false, nil
	}
	var result sessionBatchResult
	if err = service.readResponse(ctx, descriptor, response, &result); err != nil {
		return sessionBatchResult{}, false, api.ErrDependencyUnavailable
	}
	return result, true, nil
}

func (service AuthService) prepareSessionRevocation(ctx context.Context, row sessionRow, reason string) (sessionRevocationPrepared, error) {
	prepared := sessionRevocationPrepared{Session: row, NextVersion: row.Version + 1}
	var err error
	for _, target := range []*string{&prepared.EventID, &prepared.OutboxID, &prepared.CommandID} {
		if *target, err = service.newID(); err != nil {
			return sessionRevocationPrepared{}, api.ErrDependencyUnavailable
		}
	}
	prepared.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: row.ActiveTenantID, ObjectID: prepared.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": row.ID, "subject_version": prepared.NextVersion, "previous_state": "active", "new_state": "revoked", "reason_code": reason})
	if err != nil {
		return sessionRevocationPrepared{}, api.ErrDependencyUnavailable
	}
	return prepared, nil
}

func (service AuthService) commitSessionRevocations(ctx context.Context, tx pgx.Tx, metadata api.AuthenticatedRequestMetadata, prepared []sessionRevocationPrepared, reason string, now time.Time) error {
	securityEventID, err := service.newID()
	if err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]any{"reason_code": reason, "session_count": len(prepared)})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'sessions_revoked',$4,$5,$6,$7,$8)`, securityEventID, metadata.TenantID, metadata.UserID, metadata.RequestID, metadata.ClientIPHash, metadata.UserAgentHash, details, now); err != nil {
		return err
	}
	actor, _ := json.Marshal(map[string]string{"kind": "user", "id": metadata.UserID})
	for _, value := range prepared {
		tag, updateErr := tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1,version=version+1,updated_at=$1 WHERE id=$2 AND user_id=$3 AND version=$4 AND revoked_at IS NULL`, now, value.Session.ID, metadata.UserID, value.Session.Version)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return api.ErrVersionConflict
		}
		if _, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: value.EventID, TenantID: value.Session.ActiveTenantID, UserID: metadata.UserID, EventType: "SessionRevoked", SchemaVersion: 1, AggregateKind: "session", AggregateID: value.Session.ID, AggregateVersion: value.NextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: securityEventID, PayloadRef: value.EventPayload.Ref, PayloadHash: value.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: value.OutboxID, CommandID: value.CommandID, CommandType: "events.publish", PayloadRef: value.EventPayload.Ref, PayloadHash: value.EventPayload.Hash}}}); appendErr != nil {
			return appendErr
		}
	}
	return nil
}

func (service AuthService) preauthorizeSession(ctx context.Context, metadata api.AuthenticatedRequestMetadata) error {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = service.authorizeSession(ctx, tx, metadata, service.now(), false); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return api.ErrDependencyUnavailable
	}
	return nil
}

func (service AuthService) authorizeSession(ctx context.Context, tx pgx.Tx, metadata api.AuthenticatedRequestMetadata, now time.Time, lock bool) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, metadata.TenantID); err != nil {
		return api.ErrDependencyUnavailable
	}
	query := `SELECT s.id FROM identity.sessions s JOIN identity.users u ON u.id=s.user_id JOIN identity.tenants t ON t.id=s.active_tenant_id JOIN identity.memberships m ON m.tenant_id=s.active_tenant_id AND m.user_id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.active_tenant_id=$3 AND s.revoked_at IS NULL AND s.expires_at>$4 AND u.status='active' AND u.email_verified_at IS NOT NULL AND t.status='active' AND m.id=$5 AND m.status='active'`
	if lock {
		query += ` FOR UPDATE OF s`
	}
	var id string
	if err := tx.QueryRow(ctx, query, metadata.SessionID, metadata.UserID, metadata.TenantID, now, metadata.MembershipID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrInvalidCredentials
		}
		return api.ErrDependencyUnavailable
	}
	return nil
}

func (service AuthService) lookupOwnedSession(ctx context.Context, userID, sessionID string) (sessionRow, error) {
	var row sessionRow
	err := service.Pool.QueryRow(ctx, `SELECT id::text,active_tenant_id::text,version,created_at,updated_at,last_seen_at,expires_at,revoked_at,device_label FROM identity.sessions WHERE id=$1 AND user_id=$2`, sessionID, userID).Scan(&row.ID, &row.ActiveTenantID, &row.Version, &row.CreatedAt, &row.UpdatedAt, &row.LastSeenAt, &row.ExpiresAt, &row.RevokedAt, &row.DeviceLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionRow{}, api.ErrResourceNotFound
	}
	if err != nil {
		return sessionRow{}, api.ErrDependencyUnavailable
	}
	return row, nil
}

func (service AuthService) lockOwnedSession(ctx context.Context, tx pgx.Tx, userID, sessionID string) (sessionRow, error) {
	var row sessionRow
	err := tx.QueryRow(ctx, `SELECT id::text,active_tenant_id::text,version,created_at,updated_at,last_seen_at,expires_at,revoked_at,device_label FROM identity.sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, sessionID, userID).Scan(&row.ID, &row.ActiveTenantID, &row.Version, &row.CreatedAt, &row.UpdatedAt, &row.LastSeenAt, &row.ExpiresAt, &row.RevokedAt, &row.DeviceLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionRow{}, api.ErrResourceNotFound
	}
	if err != nil {
		return sessionRow{}, api.ErrDependencyUnavailable
	}
	return row, nil
}

func (service AuthService) lookupActiveOwnedSessions(ctx context.Context, userID string) ([]sessionRow, error) {
	rows, err := service.Pool.Query(ctx, `SELECT id::text,active_tenant_id::text,version,created_at,updated_at,last_seen_at,expires_at,revoked_at,device_label FROM identity.sessions WHERE user_id=$1 AND revoked_at IS NULL ORDER BY id`, userID)
	if err != nil {
		return nil, api.ErrDependencyUnavailable
	}
	defer rows.Close()
	return scanSessions(rows)
}

func (service AuthService) lockActiveOwnedSessions(ctx context.Context, tx pgx.Tx, userID string) ([]sessionRow, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,active_tenant_id::text,version,created_at,updated_at,last_seen_at,expires_at,revoked_at,device_label FROM identity.sessions WHERE user_id=$1 AND revoked_at IS NULL ORDER BY id FOR UPDATE`, userID)
	if err != nil {
		return nil, api.ErrDependencyUnavailable
	}
	defer rows.Close()
	return scanSessions(rows)
}

type sessionRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanSessions(rows sessionRows) ([]sessionRow, error) {
	values := make([]sessionRow, 0)
	for rows.Next() {
		var row sessionRow
		if err := rows.Scan(&row.ID, &row.ActiveTenantID, &row.Version, &row.CreatedAt, &row.UpdatedAt, &row.LastSeenAt, &row.ExpiresAt, &row.RevokedAt, &row.DeviceLabel); err != nil {
			return nil, api.ErrDependencyUnavailable
		}
		values = append(values, row)
	}
	if rows.Err() != nil {
		return nil, api.ErrDependencyUnavailable
	}
	return values, nil
}

func sameSessionSnapshot(prepared []sessionRevocationPrepared, actual []sessionRow) bool {
	if len(prepared) != len(actual) {
		return false
	}
	expected := make(map[string]uint64, len(prepared))
	for _, value := range prepared {
		expected[value.Session.ID] = value.Session.Version
	}
	for _, value := range actual {
		if version, exists := expected[value.ID]; !exists || version != value.Version {
			return false
		}
	}
	return true
}

func (service AuthService) authenticatedIdempotency(operation, userID, rawKey string, request any) (string, string, error) {
	recordID, err := service.idempotencyRecordID(operation, userID, rawKey)
	if err != nil {
		return "", "", err
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return "", "", err
	}
	return recordID, idempotency.RequestHash(canonical), nil
}

func (service AuthService) validateSessionRequest(metadata api.AuthenticatedRequestMetadata) error {
	if err := service.validate(); err != nil || metadata.RequestID == "" || metadata.UserID == "" || metadata.TenantID == "" || metadata.MembershipID == "" || metadata.SessionID == "" {
		return api.ErrDependencyUnavailable
	}
	return nil
}

func (service AuthService) validateSessionMutation(metadata api.AuthenticatedRequestMetadata) error {
	if err := service.validateSessionRequest(metadata); err != nil {
		return err
	}
	if metadata.ClientRequestID == "" || metadata.IdempotencyKey == "" || len(metadata.ClientIPHash) != 32 || len(metadata.UserAgentHash) != 32 {
		return api.ErrValidation
	}
	return nil
}

func (service AuthService) sessionCursorScope(userID string) string {
	return "identity.sessions:" + userID
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

var _ api.SessionService = AuthService{}
