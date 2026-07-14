package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/api"
	identityemail "github.com/langshift/lites/internal/identity/email"
	"github.com/langshift/lites/internal/payload"
)

type invitationPrepared struct {
	InvitationID, SecurityEventID, EventID, PublishOutboxID, PublishCommandID string
	MailOutboxID, MailCommandID                                               string
	TokenDigest                                                               []byte
	ExpiresAt                                                                 time.Time
	EventPayload, MailCommand, Response                                       payload.Manifest
}

type storedInvitationMutation struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func validInvitationRole(value string) bool {
	switch value {
	case "admin", "contract_admin", "program_manager", "reviewer", "member":
		return true
	default:
		return false
	}
}

func (service AuthService) CreateInvitation(ctx context.Context, command api.InvitationCreateCommand) (api.InvitationMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.InvitationMutationResult{}, err
	}
	normalizedEmail, normalizeErr := identityemail.Normalize(command.NormalizedEmail)
	if normalizeErr != nil || normalizedEmail != command.NormalizedEmail || !validInvitationRole(command.Role) || command.ExpiresInDays < 1 || command.ExpiresInDays > 7 {
		return api.InvitationMutationResult{}, api.ErrValidation
	}
	recordID, requestHash, err := service.authenticatedIdempotency("invitations.create", command.UserID, command.IdempotencyKey, struct {
		Email, Role string
		Days        int
	}{command.NormalizedEmail, command.Role, command.ExpiresInDays})
	if err != nil {
		return api.InvitationMutationResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "invitations.create"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadInvitationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeTenantAdmin(ctx, command.AuthenticatedRequestMetadata, false); err != nil {
		return service.invitationReplayOr(ctx, input, descriptor, err)
	}
	now := service.now()
	prepared, err := service.prepareInvitation(ctx, command, recordID, now)
	if err != nil {
		return api.InvitationMutationResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, lockErr := tx.Exec(ctx, `SELECT id FROM identity.users WHERE id=$1 FOR UPDATE`, command.UserID); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if authErr := service.authorizeSession(ctx, tx, command.AuthenticatedRequestMetadata, now, true); authErr != nil {
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
		if _, lockErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, command.TenantID+"|"+command.NormalizedEmail); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		var existing string
		existingErr := tx.QueryRow(ctx, `SELECT i.id::text FROM identity.invitations i WHERE i.tenant_id=$1 AND i.normalized_email=$2 AND i.accepted_at IS NULL AND i.rejected_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>$3 LIMIT 1 FOR UPDATE`, command.TenantID, command.NormalizedEmail, now).Scan(&existing)
		if existingErr == nil {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return idempotency.Response{}, existingErr
		}
		var member string
		memberErr := tx.QueryRow(ctx, `SELECT m.id::text FROM identity.memberships m JOIN identity.users u ON u.id=m.user_id WHERE m.tenant_id=$1 AND u.normalized_email=$2 AND m.status='active' LIMIT 1`, command.TenantID, command.NormalizedEmail).Scan(&member)
		if memberErr == nil {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if !errors.Is(memberErr, pgx.ErrNoRows) {
			return idempotency.Response{}, memberErr
		}
		if _, insertErr := tx.Exec(ctx, `INSERT INTO identity.invitations (id,tenant_id,normalized_email,role,token_hash,invited_by,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, prepared.InvitationID, command.TenantID, command.NormalizedEmail, command.Role, prepared.TokenDigest, command.UserID, prepared.ExpiresAt); insertErr != nil {
			return idempotency.Response{}, insertErr
		}
		if _, auditErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'invitation_created',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, command.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"role_assigned":true}`), now); auditErr != nil {
			return idempotency.Response{}, auditErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		_, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "InvitationCreated", SchemaVersion: 1, AggregateKind: "invitation", AggregateID: prepared.InvitationID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.MailOutboxID, CommandID: prepared.MailCommandID, CommandType: "identity.email.invitation", PayloadRef: prepared.MailCommand.Ref, PayloadHash: prepared.MailCommand.Hash}}})
		if appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return service.invitationReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readInvitationMutation(ctx, descriptor, response)
}

func (service AuthService) prepareInvitation(ctx context.Context, command api.InvitationCreateCommand, recordID string, now time.Time) (invitationPrepared, error) {
	var p invitationPrepared
	for _, target := range []*string{&p.InvitationID, &p.SecurityEventID, &p.EventID, &p.PublishOutboxID, &p.PublishCommandID, &p.MailOutboxID, &p.MailCommandID} {
		value, err := service.newID()
		if err != nil {
			return invitationPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	manager := service.InvitationTokens
	if manager.Random == nil {
		manager.Random = service.random()
	}
	token, err := manager.Issue()
	if err != nil {
		return invitationPrepared{}, api.ErrDependencyUnavailable
	}
	p.TokenDigest = append([]byte(nil), token.Digest[:]...)
	p.ExpiresAt = now.Add(time.Duration(command.ExpiresInDays) * 24 * time.Hour)
	p.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: p.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": p.InvitationID, "subject_version": 1})
	if err == nil {
		p.MailCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: p.MailCommandID, Class: "mail-command", ContentType: "application/json"}, map[string]any{"template": "tenant-invitation-v1", "recipient": command.NormalizedEmail, "invitation_id": p.InvitationID, "token": token.Raw, "expires_at": p.ExpiresAt})
	}
	if err == nil {
		p.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedInvitationMutation{p.InvitationID, 1, "pending", now})
	}
	if err != nil {
		return invitationPrepared{}, api.ErrDependencyUnavailable
	}
	return p, nil
}

type invitationImportPrepared struct {
	ImportID, SecurityEventID, EventID, PublishOutboxID, PublishCommandID, WorkOutboxID, WorkCommandID string
	EventPayload, WorkCommand, Response                                                                payload.Manifest
}

func (service AuthService) ImportInvitations(ctx context.Context, command api.InvitationImportCommand) (api.InvitationMutationResult, error) {
	if err := service.validateSessionMutation(command.AuthenticatedRequestMetadata); err != nil {
		return api.InvitationMutationResult{}, err
	}
	if !validBoundImportMetadata(command.ObjectRef, command.ContentHash, command.ImportKey) || !validInvitationRole(command.DefaultRole) {
		return api.InvitationMutationResult{}, api.ErrValidation
	}
	recordID, requestHash, err := service.authenticatedIdempotency("invitations.import_csv", command.UserID, command.IdempotencyKey, struct{ ObjectRef, ContentHash, ImportKey, DefaultRole string }{command.ObjectRef, command.ContentHash, command.ImportKey, command.DefaultRole})
	if err != nil {
		return api.InvitationMutationResult{}, api.ErrIdempotencyConflict
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: "invitations.import_csv"}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	if result, found, replayErr := service.loadInvitationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	if err = service.preauthorizeTenantAdmin(ctx, command.AuthenticatedRequestMetadata, true); err != nil {
		return service.invitationReplayOr(ctx, input, descriptor, err)
	}
	now := service.now()
	prepared, err := service.prepareInvitationImport(ctx, command, recordID, now)
	if err != nil {
		return api.InvitationMutationResult{}, err
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
		if _, insertErr := tx.Exec(ctx, `INSERT INTO identity.invitation_imports (id,tenant_id,initiated_by,status,object_ref,content_hash,import_key,default_role) VALUES ($1,$2,$3,'queued',$4,$5,$6,$7)`, prepared.ImportID, command.TenantID, command.UserID, command.ObjectRef, command.ContentHash, command.ImportKey, command.DefaultRole); insertErr != nil {
			return idempotency.Response{}, insertErr
		}
		if _, auditErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,'invitation_import_queued',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, command.TenantID, command.UserID, command.RequestID, command.ClientIPHash, command.UserAgentHash, json.RawMessage(`{"source_bound":true}`), now); auditErr != nil {
			return idempotency.Response{}, auditErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": command.UserID})
		_, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "InvitationImportQueued", SchemaVersion: 1, AggregateKind: "invitation_import", AggregateID: prepared.ImportID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.PublishOutboxID, CommandID: prepared.PublishCommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, {ID: prepared.WorkOutboxID, CommandID: prepared.WorkCommandID, CommandType: "identity.invitation_import.process", PayloadRef: prepared.WorkCommand.Ref, PayloadHash: prepared.WorkCommand.Hash}}})
		if appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return service.invitationReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readInvitationMutation(ctx, descriptor, response)
}

func (service AuthService) prepareInvitationImport(ctx context.Context, c api.InvitationImportCommand, recordID string, now time.Time) (invitationImportPrepared, error) {
	var p invitationImportPrepared
	for _, target := range []*string{&p.ImportID, &p.SecurityEventID, &p.EventID, &p.PublishOutboxID, &p.PublishCommandID, &p.WorkOutboxID, &p.WorkCommandID} {
		value, err := service.newID()
		if err != nil {
			return invitationImportPrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	var err error
	p.EventPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: c.TenantID, ObjectID: p.EventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": p.ImportID, "subject_version": 1})
	if err == nil {
		p.WorkCommand, err = service.putJSON(ctx, payload.Descriptor{TenantID: c.TenantID, ObjectID: p.WorkCommandID, Class: "invitation-import-command", ContentType: "application/json"}, map[string]any{"import_id": p.ImportID, "object_ref": c.ObjectRef, "content_hash": c.ContentHash, "import_key": c.ImportKey, "default_role": c.DefaultRole})
	}
	if err == nil {
		p.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: c.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedInvitationMutation{p.ImportID, 1, "queued", now})
	}
	if err != nil {
		return invitationImportPrepared{}, api.ErrDependencyUnavailable
	}
	return p, nil
}

type invitationAcceptanceLookup struct {
	InvitationID, TenantID, Email, Role string
	Version                             uint64
	ExpiresAt                           time.Time
	AcceptedAt, RejectedAt, RevokedAt   *time.Time
}
type invitationAcceptancePrepared struct {
	MembershipID, SecurityEventID, InvitationEventID, InvitationOutboxID, InvitationCommandID, MembershipEventID, MembershipOutboxID, MembershipCommandID string
	InvitationPayload, MembershipPayload, Response                                                                                                        payload.Manifest
}

func (service AuthService) AcceptInvitation(ctx context.Context, c api.InvitationAcceptCommand) (api.InvitationMutationResult, error) {
	if err := service.validateSessionMutation(c.AuthenticatedRequestMetadata); err != nil {
		return api.InvitationMutationResult{}, err
	}
	if c.InvitationID == "" || c.Token == "" || c.ExpectedVersion == 0 {
		return api.InvitationMutationResult{}, api.ErrValidation
	}
	if err := service.preauthorizeSession(ctx, c.AuthenticatedRequestMetadata); err != nil {
		return api.InvitationMutationResult{}, err
	}
	digest, err := service.InvitationTokens.Digest(c.Token)
	if err != nil {
		return api.InvitationMutationResult{}, api.ErrInvalidCredentials
	}
	lookup, err := service.lookupInvitationCapability(ctx, c.InvitationID, digest[:])
	if err != nil {
		return api.InvitationMutationResult{}, mapLookupError(err)
	}
	principalDigest := hex.EncodeToString(digest[:])
	recordID, err := service.idempotencyRecordID("invitations.accept", c.UserID, c.IdempotencyKey)
	if err != nil {
		return api.InvitationMutationResult{}, api.ErrIdempotencyConflict
	}
	canonical, _ := json.Marshal(struct {
		InvitationID, TokenDigest string
		Version                   uint64
	}{c.InvitationID, principalDigest, c.ExpectedVersion})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return api.InvitationMutationResult{}, api.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: lookup.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: lookup.TenantID, UserID: c.UserID, OperationID: "invitations.accept"}, RawKey: c.IdempotencyKey, RequestHash: requestHash, RequestID: c.RequestID}
	if result, found, replayErr := service.loadInvitationReplay(ctx, input, descriptor); replayErr != nil || found {
		return result, replayErr
	}
	now := service.now()
	if lookup.Version != c.ExpectedVersion {
		return service.invitationReplayOr(ctx, input, descriptor, api.ErrVersionConflict)
	}
	if lookup.AcceptedAt != nil || lookup.RejectedAt != nil || lookup.RevokedAt != nil || !lookup.ExpiresAt.After(now) {
		return service.invitationReplayOr(ctx, input, descriptor, api.ErrStateConflict)
	}
	prepared, err := service.prepareInvitationAcceptance(ctx, c, lookup, recordID, now)
	if err != nil {
		return api.InvitationMutationResult{}, err
	}
	response, _, err := service.idempotencyExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		var currentEmail, status string
		if lockErr := tx.QueryRow(ctx, `SELECT normalized_email,status FROM identity.users WHERE id=$1 FOR UPDATE`, c.UserID).Scan(&currentEmail, &status); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if authErr := service.authorizeSession(ctx, tx, c.AuthenticatedRequestMetadata, now, true); authErr != nil {
			return idempotency.Response{}, authErr
		}
		if _, setErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, lookup.TenantID); setErr != nil {
			return idempotency.Response{}, setErr
		}
		var tenantActive bool
		if lockErr := tx.QueryRow(ctx, `SELECT identity.lock_active_tenant($1)`, lookup.TenantID).Scan(&tenantActive); lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if !tenantActive {
			return idempotency.Response{}, api.ErrStateConflict
		}
		var version uint64
		var email, role string
		var expires time.Time
		var accepted, rejected, revoked *time.Time
		lockErr := tx.QueryRow(ctx, `SELECT version,normalized_email,role,expires_at,accepted_at,rejected_at,revoked_at FROM identity.invitations WHERE id=$1 AND token_hash=$2 FOR UPDATE`, lookup.InvitationID, digest[:]).Scan(&version, &email, &role, &expires, &accepted, &rejected, &revoked)
		if errors.Is(lockErr, pgx.ErrNoRows) || status != "active" || currentEmail != email {
			return idempotency.Response{}, api.ErrInvalidCredentials
		}
		if lockErr != nil {
			return idempotency.Response{}, lockErr
		}
		if version != c.ExpectedVersion {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if accepted != nil || rejected != nil || revoked != nil || !expires.After(now) {
			return idempotency.Response{}, api.ErrStateConflict
		}
		var membership string
		memberErr := tx.QueryRow(ctx, `SELECT id::text FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2 FOR UPDATE`, lookup.TenantID, c.UserID).Scan(&membership)
		if memberErr == nil {
			return idempotency.Response{}, api.ErrStateConflict
		}
		if !errors.Is(memberErr, pgx.ErrNoRows) {
			return idempotency.Response{}, memberErr
		}
		var nextVersion uint64
		if updateErr := tx.QueryRow(ctx, `UPDATE identity.invitations SET accepted_at=$1,version=version+1,updated_at=$1 WHERE id=$2 AND version=$3 RETURNING version`, now, lookup.InvitationID, c.ExpectedVersion).Scan(&nextVersion); updateErr != nil {
			return idempotency.Response{}, api.ErrVersionConflict
		}
		if _, insertErr := tx.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,$4,'active',$5)`, prepared.MembershipID, lookup.TenantID, c.UserID, role, now); insertErr != nil {
			return idempotency.Response{}, insertErr
		}
		if _, auditErr := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,ip_hash,user_agent_hash,details,occurred_at) VALUES ($1,$2,$3,$3,'invitation_accepted',$4,$5,$6,$7,$8)`, prepared.SecurityEventID, lookup.TenantID, c.UserID, c.RequestID, c.ClientIPHash, c.UserAgentHash, json.RawMessage(`{"membership_created":true}`), now); auditErr != nil {
			return idempotency.Response{}, auditErr
		}
		actor, _ := json.Marshal(map[string]string{"kind": "user", "id": c.UserID})
		if _, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.InvitationEventID, TenantID: lookup.TenantID, UserID: c.UserID, EventType: "InvitationAccepted", SchemaVersion: 1, AggregateKind: "invitation", AggregateID: lookup.InvitationID, AggregateVersion: nextVersion, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.InvitationPayload.Ref, PayloadHash: prepared.InvitationPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.InvitationOutboxID, CommandID: prepared.InvitationCommandID, CommandType: "events.publish", PayloadRef: prepared.InvitationPayload.Ref, PayloadHash: prepared.InvitationPayload.Hash}}}); appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		if _, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.MembershipEventID, TenantID: lookup.TenantID, UserID: c.UserID, EventType: "MembershipCreated", SchemaVersion: 1, AggregateKind: "membership", AggregateID: prepared.MembershipID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: prepared.SecurityEventID, PayloadRef: prepared.MembershipPayload.Ref, PayloadHash: prepared.MembershipPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.MembershipOutboxID, CommandID: prepared.MembershipCommandID, CommandType: "events.publish", PayloadRef: prepared.MembershipPayload.Ref, PayloadHash: prepared.MembershipPayload.Hash}}}); appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		return idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: prepared.Response.Ref, Hash: prepared.Response.Hash, ResourceVersion: nextVersion}, nil
	})
	if err != nil {
		return service.invitationReplayOr(ctx, input, descriptor, mapIdentityError(err))
	}
	return service.readInvitationMutation(ctx, descriptor, response)
}

func (service AuthService) lookupInvitationCapability(ctx context.Context, id string, digest []byte) (invitationAcceptanceLookup, error) {
	var v invitationAcceptanceLookup
	err := service.Pool.QueryRow(ctx, `SELECT invitation_id,tenant_id,normalized_email,role,version,expires_at,accepted_at,rejected_at,revoked_at FROM identity.lookup_invitation_for_acceptance($1,$2)`, id, digest).Scan(&v.InvitationID, &v.TenantID, &v.Email, &v.Role, &v.Version, &v.ExpiresAt, &v.AcceptedAt, &v.RejectedAt, &v.RevokedAt)
	return v, err
}

func (service AuthService) prepareInvitationAcceptance(ctx context.Context, c api.InvitationAcceptCommand, l invitationAcceptanceLookup, recordID string, now time.Time) (invitationAcceptancePrepared, error) {
	var p invitationAcceptancePrepared
	for _, target := range []*string{&p.MembershipID, &p.SecurityEventID, &p.InvitationEventID, &p.InvitationOutboxID, &p.InvitationCommandID, &p.MembershipEventID, &p.MembershipOutboxID, &p.MembershipCommandID} {
		value, err := service.newID()
		if err != nil {
			return invitationAcceptancePrepared{}, api.ErrDependencyUnavailable
		}
		*target = value
	}
	var err error
	p.InvitationPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: l.TenantID, ObjectID: p.InvitationEventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": l.InvitationID, "subject_version": l.Version + 1, "request_id": c.RequestID})
	if err == nil {
		p.MembershipPayload, err = service.putJSON(ctx, payload.Descriptor{TenantID: l.TenantID, ObjectID: p.MembershipEventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": p.MembershipID, "subject_version": 1})
	}
	if err == nil {
		p.Response, err = service.putJSON(ctx, payload.Descriptor{TenantID: l.TenantID, ObjectID: recordID, Class: "identity-idempotency", ContentType: "application/json"}, storedInvitationMutation{l.InvitationID, l.Version + 1, "accepted", now})
	}
	if err != nil {
		return invitationAcceptancePrepared{}, api.ErrDependencyUnavailable
	}
	return p, nil
}

func (service AuthService) preauthorizeTenantAdmin(ctx context.Context, m api.AuthenticatedRequestMetadata, recent bool) error {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return api.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.now()
	if err = service.authorizeSession(ctx, tx, m, now, false); err != nil {
		return err
	}
	if recent {
		if err = service.requireRecentReauthentication(ctx, tx, m, now); err != nil {
			return err
		}
	}
	if err = service.requireTenantAdmin(ctx, tx, m); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return api.ErrDependencyUnavailable
	}
	return nil
}
func (service AuthService) requireTenantAdmin(ctx context.Context, tx pgx.Tx, m api.AuthenticatedRequestMetadata) error {
	var role string
	err := tx.QueryRow(ctx, `SELECT role FROM identity.memberships WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND status='active'`, m.MembershipID, m.TenantID, m.UserID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.ErrPermissionDenied
	}
	if err != nil {
		return api.ErrDependencyUnavailable
	}
	if role != "owner" && role != "admin" {
		return api.ErrPermissionDenied
	}
	return nil
}

func (service AuthService) readInvitationMutation(ctx context.Context, d payload.Descriptor, r idempotency.Response) (api.InvitationMutationResult, error) {
	var s storedInvitationMutation
	if err := service.readResponse(ctx, d, r, &s); err != nil {
		return api.InvitationMutationResult{}, api.ErrDependencyUnavailable
	}
	return api.InvitationMutationResult{ID: s.ID, Version: s.Version, Status: s.Status, UpdatedAt: s.UpdatedAt}, nil
}
func (service AuthService) loadInvitationReplay(ctx context.Context, i IdempotencyInput, d payload.Descriptor) (api.InvitationMutationResult, bool, error) {
	r, found, err := service.idempotencyExecutor().LoadCompleted(ctx, i)
	if err != nil {
		return api.InvitationMutationResult{}, false, mapIdentityError(err)
	}
	if !found {
		return api.InvitationMutationResult{}, false, nil
	}
	result, err := service.readInvitationMutation(ctx, d, r)
	return result, true, err
}
func (service AuthService) invitationReplayOr(ctx context.Context, i IdempotencyInput, d payload.Descriptor, fallback error) (api.InvitationMutationResult, error) {
	if result, found, err := service.loadInvitationReplay(ctx, i, d); err != nil || found {
		return result, err
	}
	return api.InvitationMutationResult{}, fallback
}

var _ api.InvitationService = AuthService{}
