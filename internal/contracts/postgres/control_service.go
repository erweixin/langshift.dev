package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const controlResourceMedia = "application/vnd.lites.admin-control-resource.v2+json"

type ControlService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                       string
	IdempotencyTTL, ProposalTTL                      time.Duration
	Now                                              func() time.Time
}

type controlMutation func(context.Context, pgx.Tx, string) (contractsapi.Resource, error)

func (service ControlService) execute(ctx context.Context, metadata contractsapi.CommandMetadata, operation string, request any, mutation controlMutation) (contractsapi.Resource, error) {
	if !service.valid() || !validMetadata(metadata) || operation == "" || mutation == nil {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	canonical, err := json.Marshal(request)
	if err != nil {
		return contractsapi.Resource{}, contractsapi.ErrValidation
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return contractsapi.Resource{}, service.mapError(err)
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "contract-idempotency:"+operation, metadata.TenantID+"\x00"+metadata.UserID+"\x00"+metadata.IdempotencyKey)
	if err != nil {
		return contractsapi.Resource{}, service.mapError(err)
	}
	descriptor := payload.Descriptor{TenantID: metadata.TenantID, ObjectID: recordID, Class: "contract-control-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: metadata.TenantID, UserID: metadata.UserID, OperationID: operation}, RawKey: metadata.IdempotencyKey, RequestHash: requestHash, RequestID: metadata.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return contractsapi.Resource{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.readResource(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		result, innerErr := mutation(ctx, tx, recordID)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		manifest, innerErr := service.putJSON(ctx, descriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: controlResourceMedia, PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.Version}, nil
	})
	if err != nil {
		return contractsapi.Resource{}, service.mapError(err)
	}
	result, err := service.readResource(ctx, descriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service ControlService) appendEvent(ctx context.Context, tx pgx.Tx, metadata contractsapi.CommandMetadata, recordID, eventType, aggregateKind, aggregateID string, aggregateVersion uint64, body any, occurredAt time.Time) (string, error) {
	eventID, err := ids.DeterministicUUID(service.IDKey, "contract-event:"+eventType, recordID)
	if err != nil {
		return "", err
	}
	manifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: metadata.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, body)
	if err != nil {
		return "", err
	}
	outboxID, _ := ids.DeterministicUUID(service.IDKey, "contract-outbox:"+eventType, recordID)
	publishID, _ := ids.DeterministicUUID(service.IDKey, "contract-publish:"+eventType, recordID)
	actor, _ := json.Marshal(map[string]string{"kind": "user", "user_id": metadata.UserID, "session_id": metadata.SessionID})
	_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: metadata.TenantID, UserID: metadata.UserID, EventType: eventType, SchemaVersion: 1, AggregateKind: aggregateKind, AggregateID: aggregateID, AggregateVersion: aggregateVersion, StoreEpoch: service.StoreEpoch, OccurredAt: occurredAt, Actor: actor, CorrelationID: metadata.RequestID, PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}}})
	return eventID, err
}

func (service ControlService) currentPrincipal(ctx context.Context, tx pgx.Tx, metadata contractsapi.CommandMetadata, now time.Time) (string, time.Time, error) {
	var role string
	var reauthenticatedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT m.role,s.reauthenticated_at FROM identity.memberships m
      JOIN identity.tenants t ON t.id=m.tenant_id
      JOIN identity.sessions s ON s.id=$4 AND s.user_id=m.user_id AND s.active_tenant_id=m.tenant_id
      WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.id=$3 AND m.status='active'
        AND m.role IN ('owner','contract_admin') AND t.kind='enterprise' AND t.status='active'
        AND s.revoked_at IS NULL AND s.expires_at>$5`, metadata.TenantID, metadata.UserID, metadata.MembershipID, metadata.SessionID, now).Scan(&role, &reauthenticatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, contractsapi.ErrPermissionDenied
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if reauthenticatedAt == nil || reauthenticatedAt.Before(now.Add(-5*time.Minute)) || reauthenticatedAt.After(now) {
		return "", time.Time{}, contractsapi.ErrReauthentication
	}
	return role, reauthenticatedAt.UTC().Truncate(time.Microsecond), nil
}

func (service ControlService) permissionSnapshot(metadata contractsapi.CommandMetadata, role string, reauthenticatedAt time.Time) string {
	return canonicalHash(map[string]any{"tenant_id": metadata.TenantID, "user_id": metadata.UserID, "membership_id": metadata.MembershipID, "session_id": metadata.SessionID, "role": role, "reauthenticated_at": reauthenticatedAt.UTC().Format(time.RFC3339Nano)})
}

func (service ControlService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service ControlService) readResource(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (contractsapi.Resource, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return contractsapi.Resource{}, err
	}
	var result contractsapi.Resource
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return contractsapi.Resource{}, payload.ErrIntegrity
	}
	return result, nil
}

func (service ControlService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service ControlService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0 && service.ProposalTTL >= time.Minute && service.ProposalTTL <= 24*time.Hour
}

func validMetadata(metadata contractsapi.CommandMetadata) bool {
	return metadata.RequestID != "" && metadata.ClientRequestID != "" && metadata.IdempotencyKey != "" && uuid.Validate(metadata.TenantID) == nil && uuid.Validate(metadata.UserID) == nil && uuid.Validate(metadata.MembershipID) == nil && uuid.Validate(metadata.SessionID) == nil
}

func canonicalHash(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func plaintextHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (service ControlService) mapError(err error) error {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, contractsapi.ErrValidation), errors.Is(err, contractsapi.ErrPermissionDenied), errors.Is(err, contractsapi.ErrReauthentication), errors.Is(err, contractsapi.ErrResourceNotFound), errors.Is(err, contractsapi.ErrStateConflict), errors.Is(err, contractsapi.ErrIdempotencyConflict), errors.Is(err, contractsapi.ErrApprovalScopeChanged):
		return err
	case errors.Is(err, pgx.ErrNoRows):
		return contractsapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return contractsapi.ErrIdempotencyConflict
	case errors.As(err, &pgErr) && (pgErr.Code == "40001" || strings.Contains(pgErr.Message, "approval scope")):
		return errors.Join(contractsapi.ErrApprovalScopeChanged, err)
	case errors.As(err, &pgErr) && pgErr.Code == "42501":
		return errors.Join(contractsapi.ErrApprovalScopeChanged, err)
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return errors.Join(contractsapi.ErrStateConflict, err)
	case errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23514" || pgErr.Code == "22023"):
		return errors.Join(contractsapi.ErrValidation, err)
	case errors.As(err, &pgErr) && pgErr.Code == "22008":
		return errors.Join(contractsapi.ErrReauthentication, err)
	default:
		return err
	}
}
