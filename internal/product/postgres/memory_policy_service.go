package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const memoryPolicyUpdateOperation = "memory_policy.update.v2"

type MemoryPolicyService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                       string
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

func (service MemoryPolicyService) Get(ctx context.Context, query productapi.MemoryPolicyQuery) (productapi.MemoryPolicyResource, error) {
	if !service.valid() || query.RequestID == "" || query.TenantID == "" || query.UserID == "" || query.SessionID == "" {
		return productapi.MemoryPolicyResource{}, productapi.ErrValidation
	}
	policyID, err := ids.DeterministicUUID(service.IDKey, "memory-policy", query.TenantID+"\x00"+query.UserID)
	if err != nil {
		return productapi.MemoryPolicyResource{}, productapi.ErrDependencyUnavailable
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return productapi.MemoryPolicyResource{}, productapi.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.MemoryPolicyResource{}, service.mapError(err)
	}
	if err = requireSettingsSession(ctx, tx, query.TenantID, query.UserID, query.SessionID, service.now(), false); err != nil {
		return productapi.MemoryPolicyResource{}, service.mapError(err)
	}
	result := defaultMemoryPolicy(policyID)
	var kinds []byte
	err = tx.QueryRow(ctx, `SELECT version,enabled,retention_days,allowed_kinds,updated_at FROM product.memory_policies WHERE tenant_id=$1 AND user_id=$2`, query.TenantID, query.UserID).Scan(&result.Version, &result.Enabled, &result.RetentionDays, &kinds, &result.UpdatedAt)
	if err == nil {
		if json.Unmarshal(kinds, &result.AllowedKinds) != nil || !validStoredMemoryPolicy(result.Enabled, result.RetentionDays, result.AllowedKinds) {
			return productapi.MemoryPolicyResource{}, service.mapError(payload.ErrIntegrity)
		}
		result.Status = memoryPolicyStatus(result.Enabled)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return productapi.MemoryPolicyResource{}, service.mapError(err)
	}
	if err = insertSettingsAudit(ctx, tx, service.IDKey, query.RequestID, query.TenantID, query.UserID, query.SessionID, "memory_policy_read", policyID, service.now()); err != nil {
		return productapi.MemoryPolicyResource{}, service.mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.MemoryPolicyResource{}, service.mapError(err)
	}
	return result, nil
}

func (service MemoryPolicyService) Update(ctx context.Context, command productapi.MemoryPolicyUpdateCommand) (productapi.MemoryPolicyResource, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.ExpectedVersion < 1 || !validStoredMemoryPolicy(command.Enabled, command.RetentionDays, command.AllowedKinds) {
		return productapi.MemoryPolicyResource{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(command)
	if err != nil {
		return productapi.MemoryPolicyResource{}, productapi.ErrValidation
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.MemoryPolicyResource{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+memoryPolicyUpdateOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.MemoryPolicyResource{}, productapi.ErrDependencyUnavailable
	}
	policyID, _ := ids.DeterministicUUID(service.IDKey, "memory-policy", command.TenantID+"\x00"+command.UserID)
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-memory-policy-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: memoryPolicyUpdateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.MemoryPolicyResource{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	now := service.now()
	kinds, _ := json.Marshal(command.AllowedKinds)
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := requireSettingsSession(ctx, tx, command.TenantID, command.UserID, command.SessionID, now, false); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var current uint64
		var previousEnabled bool
		var previousRetention *int
		var previousKinds []byte
		innerErr := tx.QueryRow(ctx, `SELECT version,enabled,retention_days,allowed_kinds FROM product.memory_policies WHERE tenant_id=$1 AND user_id=$2 FOR UPDATE`, command.TenantID, command.UserID).Scan(&current, &previousEnabled, &previousRetention, &previousKinds)
		missing := errors.Is(innerErr, pgx.ErrNoRows)
		if innerErr != nil && !missing {
			return idempotency.Response{}, innerErr
		}
		if missing {
			current, previousEnabled, previousRetention, previousKinds = 1, true, intPointer(365), []byte(`["preference","goal","capability_context","learning_history"]`)
		}
		if current != command.ExpectedVersion {
			return idempotency.Response{}, ErrRouteConflict
		}
		next := current + 1
		if missing {
			_, innerErr = tx.Exec(ctx, `INSERT INTO product.memory_policies(tenant_id,user_id,version,enabled,retention_days,allowed_kinds,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, command.TenantID, command.UserID, next, command.Enabled, command.RetentionDays, kinds, now)
		} else {
			var tag pgconnCommandTag
			tag, innerErr = execTag(ctx, tx, `UPDATE product.memory_policies SET version=$1,enabled=$2,retention_days=$3,allowed_kinds=$4,updated_at=$5 WHERE tenant_id=$6 AND user_id=$7 AND version=$8`, next, command.Enabled, command.RetentionDays, kinds, now, command.TenantID, command.UserID, current)
			if innerErr == nil && tag.RowsAffected() != 1 {
				innerErr = ErrRouteConflict
			}
		}
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if missing {
			initialPayload, putErr := service.policyPayload(ctx, command.TenantID, policyID, 1, previousEnabled, previousRetention, previousKinds)
			if putErr != nil {
				return idempotency.Response{}, putErr
			}
			if appendErr := service.appendPolicyEvent(ctx, tx, command, policyID, recordID+":initial", 1, "initialized", nil, previousEnabled, initialPayload.Ref, now); appendErr != nil {
				return idempotency.Response{}, appendErr
			}
		}
		policyPayload, putErr := service.policyPayload(ctx, command.TenantID, policyID, next, command.Enabled, command.RetentionDays, kinds)
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		if appendErr := service.appendPolicyEvent(ctx, tx, command, policyID, recordID, next, "user_updated", boolState(previousEnabled), command.Enabled, policyPayload.Ref, now); appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		if innerErr = insertSettingsAudit(ctx, tx, service.IDKey, command.RequestID, command.TenantID, command.UserID, command.SessionID, "memory_policy_updated", policyID, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.MemoryPolicyResource{ID: policyID, Version: next, Status: memoryPolicyStatus(command.Enabled), Enabled: command.Enabled, RetentionDays: command.RetentionDays, AllowedKinds: append([]string(nil), command.AllowedKinds...), UpdatedAt: now}
		return service.store(ctx, descriptor, result)
	})
	if err != nil {
		return productapi.MemoryPolicyResource{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

// pgx.CommandTag is deliberately hidden behind this tiny local interface so
// update paths remain easy to fault-inject in service tests.
type pgconnCommandTag interface{ RowsAffected() int64 }

func execTag(ctx context.Context, tx pgx.Tx, sql string, arguments ...any) (pgconnCommandTag, error) {
	return tx.Exec(ctx, sql, arguments...)
}

func (service MemoryPolicyService) appendPolicyEvent(ctx context.Context, tx pgx.Tx, command productapi.MemoryPolicyUpdateCommand, policyID, seed string, version uint64, reason string, previous any, enabled bool, policyRef string, now time.Time) error {
	eventID, _ := ids.DeterministicUUID(service.IDKey, "memory-policy:event", seed)
	outboxID, _ := ids.DeterministicUUID(service.IDKey, "memory-policy:outbox", seed)
	publishID, _ := ids.DeterministicUUID(service.IDKey, "memory-policy:publish", seed)
	eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": policyID, "subject_version": version, "previous_state": previous, "new_state": memoryPolicyStatus(enabled), "reason_code": reason, "payload_ref": policyRef})
	if err != nil {
		return err
	}
	_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "MemoryPolicyChanged", SchemaVersion: 1, AggregateKind: "memory_policy", AggregateID: policyID, AggregateVersion: version, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
	return err
}

func (service MemoryPolicyService) policyPayload(ctx context.Context, tenantID, policyID string, version uint64, enabled bool, retention *int, kinds []byte) (payload.Manifest, error) {
	var decoded []string
	if json.Unmarshal(kinds, &decoded) != nil {
		return payload.Manifest{}, payload.ErrIntegrity
	}
	revisionID, err := ids.DeterministicUUID(service.IDKey, "memory-policy:revision", policyID+"\x00"+strconv.FormatUint(version, 10))
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: revisionID, Class: "memory-policy", ContentType: "application/json"}, map[string]any{"schema_version": 1, "policy_id": policyID, "version": version, "enabled": enabled, "retention_days": retention, "allowed_kinds": decoded})
}

func (service MemoryPolicyService) store(ctx context.Context, descriptor payload.Descriptor, result productapi.MemoryPolicyResource) (idempotency.Response, error) {
	manifest, err := service.putJSON(ctx, descriptor, result)
	if err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: http.StatusOK, ContentType: "application/json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.Version}, nil
}
func (service MemoryPolicyService) read(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.MemoryPolicyResource, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.MemoryPolicyResource{}, err
	}
	var result productapi.MemoryPolicyResource
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}
func (service MemoryPolicyService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}
func (service MemoryPolicyService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}
func (service MemoryPolicyService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (service MemoryPolicyService) mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errSettingsReauthentication):
		return productapi.ErrReauthentication
	case errors.Is(err, errSettingsPermission):
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
func defaultMemoryPolicy(id string) productapi.MemoryPolicyResource {
	return productapi.MemoryPolicyResource{ID: id, Version: 1, Status: "enabled", Enabled: true, RetentionDays: intPointer(365), AllowedKinds: []string{"preference", "goal", "capability_context", "learning_history"}, UpdatedAt: time.Unix(0, 0).UTC()}
}
func validStoredMemoryPolicy(enabled bool, retention *int, kinds []string) bool {
	if !enabled {
		return retention == nil && len(kinds) == 0
	}
	if retention == nil || *retention < 1 || *retention > 3650 || len(kinds) < 1 || len(kinds) > 4 {
		return false
	}
	allowed := []string{"preference", "goal", "capability_context", "learning_history"}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if !slices.Contains(allowed, kind) || seen[kind] {
			return false
		}
		seen[kind] = true
	}
	return true
}
func memoryPolicyStatus(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
func boolState(value bool) any  { return memoryPolicyStatus(value) }
func intPointer(value int) *int { return &value }

var _ productapi.MemoryPolicyService = MemoryPolicyService{}
