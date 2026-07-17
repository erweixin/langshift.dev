package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	shareGrantCreateOperation = "share-grants.create.v2"
	shareGrantRevokeOperation = "share-grants.revoke.v2"
)

var errSharePermission = errors.New("share grant permission denied")

type ShareGrantService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                       string
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

func (service ShareGrantService) Create(ctx context.Context, command productapi.CreateShareGrantCommand) (productapi.ShareGrantResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || uuid.Validate(command.GranteeUserID) != nil || command.GranteeUserID == command.UserID || !validShareServiceResource(command.ResourceKind, command.ResourceID, command.ResourceRevision) || !validShareServiceScope(command.Scope) || command.ExpiresAt != nil && !command.ExpiresAt.After(service.now()) {
		return productapi.ShareGrantResult{}, productapi.ErrValidation
	}
	command.Scope = append([]string(nil), command.Scope...)
	sort.Strings(command.Scope)
	canonical, _ := json.Marshal(command)
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.ShareGrantResult{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+shareGrantCreateOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.ShareGrantResult{}, productapi.ErrDependencyUnavailable
	}
	grantID, _ := ids.DeterministicUUID(service.IDKey, "share-grant", recordID)
	eventID, _ := ids.DeterministicUUID(service.IDKey, "share-grant-created:event", recordID)
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-share-grant-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: shareGrantCreateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.ShareGrantResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	eventManifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"grant_id": grantID, "grantee_user_id": command.GranteeUserID, "resource_kind": command.ResourceKind, "resource_id": command.ResourceID, "resource_revision": command.ResourceRevision, "scope": command.Scope, "expires_at": command.ExpiresAt})
	if err != nil {
		return productapi.ShareGrantResult{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := service.requireActiveMembers(ctx, tx, command.TenantID, command.UserID, command.GranteeUserID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := service.requireOwnedRevision(ctx, tx, command.TenantID, command.UserID, command.ResourceKind, command.ResourceID, command.ResourceRevision); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		now := service.now()
		scope, _ := json.Marshal(command.Scope)
		_, innerErr := tx.Exec(ctx, `INSERT INTO product.share_grants(id,tenant_id,user_id,version,created_at,updated_at,grantee_user_id,resource_kind,resource_id,resource_revision,scope,expires_at) VALUES($1,$2,$3,1,$4,$4,$5,$6,$7,$8,$9,$10)`, grantID, command.TenantID, command.UserID, now, command.GranteeUserID, command.ResourceKind, command.ResourceID, command.ResourceRevision, scope, command.ExpiresAt)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "share-grant-created:outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "share-grant-created:publish", recordID)
		_, innerErr = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "ShareGrantCreated", SchemaVersion: 1, AggregateKind: "share_grant", AggregateID: grantID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}}})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.ShareGrantResult{ID: grantID, Version: 1, Status: "active", UpdatedAt: now}
		stored, innerErr := service.putJSON(ctx, descriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.share-grant.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return productapi.ShareGrantResult{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service ShareGrantService) Revoke(ctx context.Context, command productapi.RevokeShareGrantCommand) (productapi.ShareGrantResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || uuid.Validate(command.GrantID) != nil || command.ExpectedGrantVersion < 1 || len(command.Reason) < 1 || len(command.Reason) > 1000 {
		return productapi.ShareGrantResult{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(command)
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.ShareGrantResult{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+shareGrantRevokeOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.ShareGrantResult{}, productapi.ErrDependencyUnavailable
	}
	eventID, _ := ids.DeterministicUUID(service.IDKey, "share-grant-revoked:event", recordID)
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-share-grant-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: shareGrantRevokeOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.ShareGrantResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	eventManifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"grant_id": command.GrantID, "expected_grant_version": command.ExpectedGrantVersion, "reason": command.Reason})
	if err != nil {
		return productapi.ShareGrantResult{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var current uint64
		innerErr := tx.QueryRow(ctx, `SELECT version FROM product.share_grants WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND revoked_at IS NULL FOR UPDATE`, command.TenantID, command.UserID, command.GrantID).Scan(&current)
		if errors.Is(innerErr, pgx.ErrNoRows) {
			return idempotency.Response{}, errSharePermission
		}
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if current != command.ExpectedGrantVersion {
			return idempotency.Response{}, ErrRouteConflict
		}
		now, next := service.now(), current+1
		tag, innerErr := tx.Exec(ctx, `UPDATE product.share_grants SET version=$1,revoked_at=$2,updated_at=$2 WHERE tenant_id=$3 AND user_id=$4 AND id=$5 AND version=$6 AND revoked_at IS NULL`, next, now, command.TenantID, command.UserID, command.GrantID, current)
		if innerErr != nil || tag.RowsAffected() != 1 {
			if innerErr == nil {
				innerErr = ErrRouteConflict
			}
			return idempotency.Response{}, innerErr
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "share-grant-revoked:outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "share-grant-revoked:publish", recordID)
		_, innerErr = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "ShareGrantRevoked", SchemaVersion: 1, AggregateKind: "share_grant", AggregateID: command.GrantID, AggregateVersion: next, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}}})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.ShareGrantResult{ID: command.GrantID, Version: next, Status: "revoked", UpdatedAt: now}
		stored, innerErr := service.putJSON(ctx, descriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.share-grant.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: next}, nil
	})
	if err != nil {
		return productapi.ShareGrantResult{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service ShareGrantService) Authorize(ctx context.Context, query productapi.ShareGrantAccessQuery) (bool, error) {
	if !service.valid() || query.TenantID == "" || query.GranteeUserID == "" || !validShareServiceResource(query.ResourceKind, query.ResourceID, query.ResourceRevision) || query.Scope != "read" && query.Scope != "review" {
		return false, productapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return false, service.mapError(err)
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM product.share_grants g
		JOIN identity.memberships owner_membership ON owner_membership.tenant_id=g.tenant_id AND owner_membership.user_id=g.user_id AND owner_membership.status='active'
		JOIN identity.memberships grantee_membership ON grantee_membership.tenant_id=g.tenant_id AND grantee_membership.user_id=g.grantee_user_id AND grantee_membership.status='active'
		WHERE g.tenant_id=$1 AND g.grantee_user_id=$2 AND g.resource_kind=$3 AND g.resource_id=$4 AND g.resource_revision=$5
		AND g.revoked_at IS NULL AND (g.expires_at IS NULL OR g.expires_at>$7) AND g.scope @> jsonb_build_array($6::text)
	)`, query.TenantID, query.GranteeUserID, query.ResourceKind, query.ResourceID, query.ResourceRevision, query.Scope, service.now()).Scan(&allowed)
	if err != nil {
		return false, service.mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return false, service.mapError(err)
	}
	return allowed, nil
}

func (service ShareGrantService) requireActiveMembers(ctx context.Context, tx pgx.Tx, tenantID, ownerID, granteeID string) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.memberships WHERE tenant_id=$1 AND user_id=ANY($2::uuid[]) AND status='active'`, tenantID, []string{ownerID, granteeID}).Scan(&count); err != nil {
		return err
	}
	if count != 2 {
		return errSharePermission
	}
	return nil
}

func (service ShareGrantService) requireOwnedRevision(ctx context.Context, tx pgx.Tx, tenantID, userID, kind, resourceID, revision string) error {
	queries := map[string]string{
		"evidence":  `SELECT EXISTS(SELECT 1 FROM product.evidence WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND version::text=$4 AND status<>'invalidated')`,
		"workspace": `SELECT EXISTS(SELECT 1 FROM product.project_workspace_bindings WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND head_revision=$4)`,
		"artifact":  `SELECT EXISTS(SELECT 1 FROM product.artifacts WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND current_revision::text=$4 AND status='ready')`,
		"project":   `SELECT EXISTS(SELECT 1 FROM product.projects WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND version::text=$4 AND status<>'archived')`,
	}
	statement, ok := queries[kind]
	if !ok {
		return productapi.ErrValidation
	}
	var exists bool
	if err := tx.QueryRow(ctx, statement, tenantID, userID, resourceID, revision).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errSharePermission
	}
	return nil
}

func validShareServiceResource(kind, id, revision string) bool {
	if kind != "evidence" && kind != "workspace" && kind != "artifact" && kind != "project" {
		return false
	}
	return uuid.Validate(id) == nil && revision != "" && len(revision) <= 500
}

func validShareServiceScope(scope []string) bool {
	if len(scope) < 1 || len(scope) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range scope {
		if value != "read" && value != "review" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func (service ShareGrantService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service ShareGrantService) read(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.ShareGrantResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.ShareGrantResult{}, err
	}
	var result productapi.ShareGrantResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}

func (service ShareGrantService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service ShareGrantService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}

func (service ShareGrantService) mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errSharePermission):
		return productapi.ErrPermissionDenied
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRouteConflict):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.ShareGrantService = ShareGrantService{}
