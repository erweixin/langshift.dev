package postgres

import (
	"context"
	"encoding/hex"
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
	"github.com/langshift/lites/internal/platform/cursor"
)

const maximumMembershipsPage = 100

type membershipRow struct {
	ID, TenantID, UserID, Email, Role, Status string
	TenantKind                                string
	Version                                   uint64
	JoinedAt, UpdatedAt                       time.Time
	DeactivatedAt                             *time.Time
}

type membershipCursor struct {
	JoinedAt time.Time `json:"joined_at"`
	ID       string    `json:"id"`
}

func (service AuthService) ListMemberships(ctx context.Context, query api.MembershipsQuery) (api.MembershipsPage, error) {
	if err := service.validateSessionRequest(query.AuthenticatedRequestMetadata); err != nil {
		return api.MembershipsPage{}, err
	}
	if query.Limit < 1 || query.Limit > maximumMembershipsPage {
		return api.MembershipsPage{}, api.ErrValidation
	}
	var after membershipCursor
	if query.Cursor != "" {
		if err := (cursor.Codec{Key: service.CursorKey}).Decode(query.Cursor, service.membershipCursorScope(query.TenantID, query.UserID), &after); err != nil || after.ID == "" || after.JoinedAt.IsZero() {
			return api.MembershipsPage{}, api.ErrValidation
		}
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return api.MembershipsPage{}, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.now()
	if err = service.authorizeSession(ctx, tx, query.AuthenticatedRequestMetadata, now, false); err != nil {
		return api.MembershipsPage{}, err
	}
	if err = service.requireTenantAdmin(ctx, tx, query.AuthenticatedRequestMetadata); err != nil {
		return api.MembershipsPage{}, err
	}
	rows, err := tx.Query(ctx, `SELECT m.id::text,m.user_id::text,u.normalized_email,m.role,m.status,m.version,m.joined_at,m.updated_at,m.deactivated_at FROM identity.memberships m JOIN identity.users u ON u.id=m.user_id WHERE m.tenant_id=$1 AND ($2::timestamptz IS NULL OR (m.joined_at,m.id)<($2,$3::uuid)) ORDER BY m.joined_at DESC,m.id DESC LIMIT $4`, query.TenantID, nullableTime(after.JoinedAt), nullableString(after.ID), query.Limit+1)
	if err != nil {
		return api.MembershipsPage{}, api.ErrDependencyUnavailable
	}
	items := make([]api.MembershipItem, 0, query.Limit+1)
	for rows.Next() {
		var item api.MembershipItem
		if err = rows.Scan(&item.ID, &item.UserID, &item.Email, &item.Role, &item.Status, &item.Version, &item.JoinedAt, &item.UpdatedAt, &item.DeactivatedAt); err != nil {
			rows.Close()
			return api.MembershipsPage{}, api.ErrDependencyUnavailable
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return api.MembershipsPage{}, api.ErrDependencyUnavailable
	}
	rows.Close()
	var next *string
	if len(items) > query.Limit {
		last := items[query.Limit-1]
		token, encodeErr := (cursor.Codec{Key: service.CursorKey}).Encode(service.membershipCursorScope(query.TenantID, query.UserID), membershipCursor{JoinedAt: last.JoinedAt, ID: last.ID})
		if encodeErr != nil {
			return api.MembershipsPage{}, api.ErrDependencyUnavailable
		}
		next = &token
		items = items[:query.Limit]
	}
	auditID, err := service.newID()
	if err != nil {
		return api.MembershipsPage{}, api.ErrDependencyUnavailable
	}
	details, _ := json.Marshal(map[string]any{"returned_count": len(items), "cursor_used": query.Cursor != ""})
	if _, err = tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'memberships_listed',$4,$5,$6,$7,$8)`, auditID, query.TenantID, query.UserID, query.RequestID, query.ClientIPHash, query.UserAgentHash, details, now); err != nil {
		return api.MembershipsPage{}, api.ErrDependencyUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return api.MembershipsPage{}, api.ErrDependencyUnavailable
	}
	return api.MembershipsPage{Items: items, NextCursor: next}, nil
}

type membershipDeactivationPrepared struct {
	Target                                membershipRow
	SecurityEventID, EventID              string
	OutboxID, CommandID                   string
	EventPayload, ReasonPayload, Response payload.Manifest
	Sessions                              []sessionRevocationPrepared
	NextVersion                           uint64
	NextStatus                            string
}

func (service AuthService) DeactivateMembership(ctx context.Context, command api.MembershipDeactivateCommand) (api.MembershipMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.MembershipMutationResult{}, err
	}
	if command.MembershipID == "" || (command.Action != "deactivate" && command.Action != "leave") || command.ExpectedVersion == 0 || !validMembershipReason(command.Reason) {
		return api.MembershipMutationResult{}, api.ErrValidation
	}
	recordID, requestHash, err := service.authenticatedIdempotency("memberships.deactivate", command.UserID, command.IdempotencyKey, struct {
		MembershipID, Action, Reason string
		ExpectedVersion              uint64
	}{command.MembershipID, command.Action, command.Reason, command.ExpectedVersion})
	if err != nil {
		return api.MembershipMutationResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "memberships.deactivate"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadMembershipReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	target, sessions, err := service.preauthorizeMembershipDeactivation(ctx, command)
	if err != nil {
		return service.membershipReplayOr(ctx, input, descriptor, err)
	}
	if target.Version != command.ExpectedVersion {
		return service.membershipReplayOr(ctx, input, descriptor, api.ErrVersionConflict)
	}
	prepared, err := service.prepareMembershipDeactivation(ctx, command, target, sessions, recordID)
	if err != nil {
		return api.MembershipMutationResult{}, err
	}
	now := service.now()
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if lockErr := lockMembershipUsers(ctx, tx, command.UserID, target.UserID); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if authErr := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); authErr != nil {
			return idempotency.Response{}, authErr
		}
		if authErr := service.requireRecentReauthentication(ctx, tx, command.AuthenticatedRequestMetadata, now); authErr != nil {
			return idempotency.Response{}, authErr
		}
		if command.Action == "deactivate" {
			if command.UserID == target.UserID {
				return idempotency.Response{}, api.ErrPermissionDenied
			}
			if roleErr := service.requireTenantAdmin(ctx, tx, command.AuthenticatedRequestMetadata); roleErr != nil {
				return idempotency.Response{}, roleErr
			}
		}
		var tenantActive bool
		if lockErr := tx.QueryRow(ctx, `SELECT identity.lock_active_tenant($1)`, command.TenantID).Scan(&tenantActive); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if !tenantActive {
			return idempotency.Response{}, api.ErrStateConflict
		}
		current, lockErr := lockMembershipRow(ctx, tx, command.TenantID, command.MembershipID)
		if lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if authErr := validateMembershipDeactivation(ctx, tx, command, current); authErr != nil {
			return idempotency.Response{}, authErr
		}
		if current.Version != command.ExpectedVersion {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		allSessions, sessionErr := service.lockActiveOwnedSessions(ctx, tx, current.UserID)
		if sessionErr != nil {
			return idempotency.Response{}, sessionErr
		}
		actualSessions := sessionsForTenant(allSessions, command.TenantID)
		if !sameSessionSnapshot(prepared.Sessions, actualSessions) {
			return idempotency.Response{}, api.ErrStateConflict
		}
		var nextVersion uint64
		if updateErr := tx.QueryRow(ctx, `UPDATE identity.memberships SET status=$1,deactivated_at=$2,version=version+1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND status='active' RETURNING version`, prepared.NextStatus, now, current.ID, command.TenantID, command.ExpectedVersion).Scan(&nextVersion); updateErr != nil {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		details, _ := json.Marshal(map[string]any{"action": command.Action, "reason_ref": prepared.ReasonPayload.Ref, "reason_hash": prepared.ReasonPayload.Hash, "revoked_session_count": len(prepared.Sessions)})
		if _, auditErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$4,'membership_deactivated',$5,$6,$7,$8,$9)`, prepared.SecurityEventID, command.TenantID, current.UserID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, details, now); auditErr != nil {
			return idempotency.Response{}, auditErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		if _, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: current.UserID, EventType: "MembershipDeactivated", SchemaVersion: 1, AggregateKind: "membership", AggregateID: current.ID, AggregateVersion: nextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.OutboxID, CommandID: prepared.CommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}}}); appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		for _, session := range prepared.Sessions {
			tag, updateErr := tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1,version=version+1,updated_at=$1 WHERE id=$2 AND user_id=$3 AND active_tenant_id=$4 AND version=$5 AND revoked_at IS NULL`, now, session.Session.ID, current.UserID, command.TenantID, session.Session.Version)
			if updateErr != nil {
				return idempotency.Response{}, updateErr
			}
			if tag.RowsAffected() != 1 {
				return idempotency.Response{}, api.ErrVersionConflict
			}
			if _, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: session.EventID, TenantID: command.TenantID, UserID: current.UserID, EventType: "SessionRevoked", SchemaVersion: 1, AggregateKind: "session", AggregateID: session.Session.ID, AggregateVersion: session.NextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: session.EventPayload.Ref, PayloadHash: session.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: session.OutboxID, CommandID: session.CommandID, CommandType: "events.publish", PayloadRef: session.EventPayload.Ref, PayloadHash: session.EventPayload.Hash}}}); appendErr != nil {
				return idempotency.Response{}, appendErr
			}
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: nextVersion}, nil
	})
	if err != nil {
		return service.membershipReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readMembershipMutation(ctx, descriptor, response)
}

func (service AuthService) preauthorizeMembershipDeactivation(ctx context.Context, command api.MembershipDeactivateCommand) (membershipRow, []sessionRow, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return membershipRow{}, nil, api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.now()
	if err = service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, false); err != nil {
		return membershipRow{}, nil, err
	}
	if err = service.requireRecentReauthentication(ctx, tx, command.AuthenticatedRequestMetadata, now); err != nil {
		return membershipRow{}, nil, err
	}
	target, err := queryMembershipRow(ctx, tx, command.TenantID, command.MembershipID, false)
	if err != nil {
		return membershipRow{}, nil, err
	}
	if command.Action == "deactivate" {
		if command.UserID == target.UserID {
			return membershipRow{}, nil, api.ErrPermissionDenied
		}
		if err = service.requireTenantAdmin(ctx, tx, command.AuthenticatedRequestMetadata); err != nil {
			return membershipRow{}, nil, err
		}
	}
	if err = validateMembershipDeactivation(ctx, tx, command, target); err != nil {
		return membershipRow{}, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,active_tenant_id::text,version,created_at,updated_at,last_seen_at,expires_at,revoked_at,device_label FROM identity.sessions WHERE user_id=$1 AND active_tenant_id=$2 AND revoked_at IS NULL ORDER BY id`, target.UserID, command.TenantID)
	if err != nil {
		return membershipRow{}, nil, api.ErrDependencyUnavailable
	}
	sessions, err := scanSessions(rows)
	rows.Close()
	if err != nil {
		return membershipRow{}, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return membershipRow{}, nil, api.ErrDependencyUnavailable
	}
	return target, sessions, nil
}

func validateMembershipDeactivation(ctx context.Context, tx pgx.Tx, command api.MembershipDeactivateCommand, target membershipRow) error {
	if target.TenantKind != "enterprise" || target.Status != "active" {
		return api.ErrStateConflict
	}
	if command.Action == "leave" && (target.UserID != command.UserID || target.ID != command.MembershipID) {
		return api.ErrPermissionDenied
	}
	if target.Role == "owner" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, command.TenantID+"|membership-owner"); err != nil {
			return api.ErrDependencyUnavailable
		}
		var owners int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.memberships WHERE tenant_id=$1 AND role='owner' AND status='active'`, command.TenantID).Scan(&owners); err != nil {
			return api.ErrDependencyUnavailable
		}
		if owners <= 1 {
			return api.ErrStateConflict
		}
	}
	return nil
}

func queryMembershipRow(ctx context.Context, tx pgx.Tx, tenantID, membershipID string, lock bool) (membershipRow, error) {
	query := `SELECT m.id::text,m.tenant_id::text,m.user_id::text,u.normalized_email,m.role,m.status,m.version,m.joined_at,m.updated_at,m.deactivated_at,t.kind FROM identity.memberships m JOIN identity.users u ON u.id=m.user_id JOIN identity.tenants t ON t.id=m.tenant_id WHERE m.id=$1 AND m.tenant_id=$2`
	if lock {
		query += ` FOR UPDATE OF m`
	}
	var row membershipRow
	err := tx.QueryRow(ctx, query, membershipID, tenantID).Scan(&row.ID, &row.TenantID, &row.UserID, &row.Email, &row.Role, &row.Status, &row.Version, &row.JoinedAt, &row.UpdatedAt, &row.DeactivatedAt, &row.TenantKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return membershipRow{}, api.ErrResourceNotFound
	}
	if err != nil {
		return membershipRow{}, api.ErrDependencyUnavailable
	}
	return row, nil
}

func lockMembershipRow(ctx context.Context, tx pgx.Tx, tenantID, membershipID string) (membershipRow, error) {
	return queryMembershipRow(ctx, tx, tenantID, membershipID, true)
}

func lockMembershipUsers(ctx context.Context, tx pgx.Tx, actorUserID, targetUserID string) error {
	rows, err := tx.Query(ctx, `SELECT id::text FROM identity.users WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE`, actorUserID, targetUserID)
	if err != nil {
		return api.ErrDependencyUnavailable
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return api.ErrDependencyUnavailable
		}
		count++
	}
	expected := 2
	if actorUserID == targetUserID {
		expected = 1
	}
	if rows.Err() != nil || count != expected {
		return api.ErrDependencyUnavailable
	}
	return nil
}

func sessionsForTenant(rows []sessionRow, tenantID string) []sessionRow {
	filtered := make([]sessionRow, 0, len(rows))
	for _, row := range rows {
		if row.ActiveTenantID == tenantID {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func (service AuthService) prepareMembershipDeactivation(ctx context.Context, command api.MembershipDeactivateCommand, target membershipRow, sessions []sessionRow, recordID string) (membershipDeactivationPrepared, error) {
	p := membershipDeactivationPrepared{Target: target, NextVersion: target.Version + 1, NextStatus: "suspended"}
	if command.Action == "leave" {
		p.NextStatus = "left"
	}
	for _, value := range []*string{&p.SecurityEventID, &p.EventID, &p.OutboxID, &p.CommandID} {
		id, err := service.newID()
		if err != nil {
			return membershipDeactivationPrepared{}, api.ErrDependencyUnavailable
		}
		*value = id
	}
	now := service.now()
	var err error
	p.ReasonPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: p.SecurityEventID, Class: "security-reason", ContentType: "application/json"}, map[string]string{"reason": command.Reason})
	if err == nil {
		p.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: p.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": target.ID, "subject_version": p.NextVersion, "previous_state": target.Status, "new_state": p.NextStatus, "reason_hash": p.ReasonPayload.Hash})
	}
	if err == nil {
		p.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedMembershipMutation{target.ID, p.NextVersion, p.NextStatus, now})
	}
	for _, row := range sessions {
		if err != nil {
			break
		}
		prepared, prepareErr := service.prepareSessionRevocation(ctx, row, "membership_"+command.Action)
		if prepareErr != nil {
			err = prepareErr
			break
		}
		p.Sessions = append(p.Sessions, prepared)
	}
	if err != nil {
		return membershipDeactivationPrepared{}, api.ErrDependencyUnavailable
	}
	return p, nil
}

type membershipImportPrepared struct {
	ImportID, SecurityEventID, EventID, PublishOutboxID, PublishCommandID, WorkOutboxID, WorkCommandID string
	EventPayload, WorkCommand, Response                                                                payload.Manifest
}

func (service AuthService) ImportMemberships(ctx context.Context, command api.MembershipImportCommand) (api.MembershipMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.MembershipMutationResult{}, err
	}
	if !validBoundImportMetadata(command.ObjectRef, command.ContentHash, command.ImportKey) || (command.Mode != "upsert" && command.Mode != "deactivate_missing") {
		return api.MembershipMutationResult{}, api.ErrValidation
	}
	recordID, requestHash, err := service.authenticatedIdempotency("memberships.import_csv", command.UserID, command.IdempotencyKey, struct{ ObjectRef, ContentHash, ImportKey, Mode string }{command.ObjectRef, command.ContentHash, command.ImportKey, command.Mode})
	if err != nil {
		return api.MembershipMutationResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "memberships.import_csv"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadMembershipReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeTenantAdmin(ctx, command.AuthenticatedRequestMetadata, true); err != nil {
		return service.membershipReplayOr(ctx, input, descriptor, err)
	}
	prepared, err := service.prepareMembershipImport(ctx, command, recordID)
	if err != nil {
		return api.MembershipMutationResult{}, err
	}
	now := service.now()
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
		if roleErr := service.requireTenantAdmin(ctx, tx, command.AuthenticatedRequestMetadata); roleErr != nil {
			return idempotency.Response{}, roleErr
		}
		var tenantActive bool
		if lockErr := tx.QueryRow(ctx, `SELECT identity.lock_active_tenant($1)`, command.TenantID).Scan(&tenantActive); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if !tenantActive {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if _, insertErr := tx.Exec(ctx, `INSERT INTO identity.membership_imports (id,tenant_id,initiated_by,status,object_ref,content_hash,import_key,mode) VALUES ($1,$2,$3,'queued',$4,$5,$6,$7)`, prepared.ImportID, command.TenantID, command.UserID, command.ObjectRef, command.ContentHash, command.ImportKey, command.Mode); insertErr != nil {
			return idempotency.Response{}, insertErr
		}
		if _, auditErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'membership_import_queued',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, command.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"source_bound":true}`), now); auditErr != nil {
			return idempotency.Response{}, auditErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		_, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "MembershipImportQueued", SchemaVersion: 1, AggregateKind: "membership_import", AggregateID: prepared.ImportID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.WorkOutboxID, CommandID: prepared.WorkCommandID, CommandType: "identity.membership_import.process", PayloadRef: prepared.WorkCommand.Ref, PayloadHash: prepared.WorkCommand.Hash}}})
		if appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return service.membershipReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readMembershipMutation(ctx, descriptor, response)
}

func (service AuthService) prepareMembershipImport(ctx context.Context, command api.MembershipImportCommand, recordID string) (membershipImportPrepared, error) {
	var p membershipImportPrepared
	for _, target := range []*string{&p.ImportID, &p.SecurityEventID, &p.EventID, &p.PublishOutboxID, &p.PublishCommandID, &p.WorkOutboxID, &p.WorkCommandID} {
		id, err := service.newID()
		if err != nil {
			return membershipImportPrepared{}, api.ErrDependencyUnavailable
		}
		*target = id
	}
	now := service.now()
	var err error
	p.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: p.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": p.ImportID, "subject_version": 1})
	if err == nil {
		p.WorkCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: p.WorkCommandID, Class: "membership-import-command", ContentType: "application/json"}, map[string]any{"import_id": p.ImportID, "object_ref": command.ObjectRef, "content_hash": command.ContentHash, "import_key": command.ImportKey, "mode": command.Mode})
	}
	if err == nil {
		p.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedMembershipMutation{p.ImportID, 1, "queued", now})
	}
	if err != nil {
		return membershipImportPrepared{}, api.ErrDependencyUnavailable
	}
	return p, nil
}

type storedMembershipMutation struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (service AuthService) readMembershipMutation(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (api.MembershipMutationResult, error) {
	var stored storedMembershipMutation
	if err := service.readResponse(ctx, descriptor, response, &stored); err != nil {
		return api.MembershipMutationResult{}, api.ErrDependencyUnavailable
	}
	return api.MembershipMutationResult{ID: stored.ID, Version: stored.Version, Status: stored.Status, UpdatedAt: stored.UpdatedAt}, nil
}

func (service AuthService) loadMembershipReplay(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor) (api.MembershipMutationResult, bool, error) {
	response, found, err := service.idempotencyExecutor().LoadCompleted(ctx, input)
	if err != nil {
		return api.MembershipMutationResult{}, false, mapIdentityError(err)
	}
	if !found {
		return api.MembershipMutationResult{}, false, nil
	}
	result, err := service.readMembershipMutation(ctx, descriptor, response)
	return result, true, err
}

func (service AuthService) membershipReplayOr(ctx context.Context, input IdempotencyInput, descriptor payload.Descriptor, fallback error) (api.MembershipMutationResult, error) {
	if result, found, err := service.loadMembershipReplay(ctx, input, descriptor); err != nil || found {
		return result, err
	}
	return api.MembershipMutationResult{}, fallback
}

func (service AuthService) membershipCursorScope(tenantID, userID string) string {
	return "identity.memberships:" + tenantID + ":" + userID
}

func validMembershipReason(value string) bool {
	return len(value) >= 1 && len(value) <= 1000 && utf8.ValidString(value) && strings.TrimSpace(value) != "" && !strings.ContainsFunc(value, unicode.IsControl)
}

func validBoundImportMetadata(objectRef, contentHash, importKey string) bool {
	return validBoundString(objectRef, 1, 2000) && validSHA256ContentHash(contentHash) && validBoundString(importKey, 16, 200)
}

func validSHA256ContentHash(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && "sha256:"+hex.EncodeToString(decoded) == value
}

func validBoundString(value string, min, max int) bool {
	return len(value) >= min && len(value) <= max && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

var _ api.MembershipService = AuthService{}
