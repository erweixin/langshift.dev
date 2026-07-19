package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/cursor"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	byokCreateOperation = "byok.create.v2"
	byokDeleteOperation = "byok.delete.v2"
)

type APIKeyVault interface {
	PutAPIKey(context.Context, string, string) (version string, created bool, err error)
	DestroySecretVersion(context.Context, string, int) error
}

type BYOKService struct {
	Pool                                                        *pgxpool.Pool
	Appender                                                    eventpostgres.Appender
	Payloads                                                    payload.Store
	Vault                                                       APIKeyVault
	SecretPrefix                                                string
	IDKey, CursorKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                                  string
	IdempotencyTTL                                              time.Duration
	Now                                                         func() time.Time
}

type byokCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        string    `json:"id"`
}
type storedBYOK struct {
	ID, ProviderID, BoundHost, SecretRef, SecretVersion, Status, SecretHint string
	Version                                                                 uint64
	LastValidatedAt                                                         *time.Time
	UpdatedAt                                                               time.Time
}

func (service BYOKService) List(ctx context.Context, query productapi.BYOKListQuery) (productapi.BYOKListResult, error) {
	if !service.valid() || query.RequestID == "" || query.TenantID == "" || query.UserID == "" || query.SessionID == "" {
		return productapi.BYOKListResult{}, productapi.ErrValidation
	}
	var after byokCursor
	if query.Cursor != "" {
		if err := (cursor.Codec{Key: service.CursorKey}).Decode(query.Cursor, "byok.list:"+query.TenantID+":"+query.UserID, &after); err != nil || after.ID == "" || after.UpdatedAt.IsZero() {
			return productapi.BYOKListResult{}, productapi.ErrValidation
		}
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return productapi.BYOKListResult{}, productapi.ErrDependencyUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.BYOKListResult{}, service.mapError(err)
	}
	if err = requireSettingsSession(ctx, tx, query.TenantID, query.UserID, query.SessionID, service.now(), true); err != nil {
		return productapi.BYOKListResult{}, service.mapError(err)
	}
	rows, err := tx.Query(ctx, `SELECT id::text,version,status,provider_id,bound_host,secret_hint,last_validated_at,updated_at FROM product.byok_credentials WHERE tenant_id=$1 AND user_id=$2 AND ($3::timestamptz IS NULL OR (updated_at,id)<($3,$4::uuid)) ORDER BY updated_at DESC,id DESC LIMIT 101`, query.TenantID, query.UserID, byokNullableTime(after.UpdatedAt), nullableString(after.ID))
	if err != nil {
		return productapi.BYOKListResult{}, service.mapError(err)
	}
	defer rows.Close()
	items := make([]productapi.BYOKCredentialResource, 0, 101)
	for rows.Next() {
		var item productapi.BYOKCredentialResource
		if err = rows.Scan(&item.ID, &item.Version, &item.Status, &item.ProviderID, &item.BoundHost, &item.SecretHint, &item.LastValidatedAt, &item.UpdatedAt); err != nil {
			return productapi.BYOKListResult{}, service.mapError(err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return productapi.BYOKListResult{}, service.mapError(err)
	}
	var next *string
	if len(items) > 100 {
		last := items[99]
		encoded, encodeErr := (cursor.Codec{Key: service.CursorKey}).Encode("byok.list:"+query.TenantID+":"+query.UserID, byokCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
		if encodeErr != nil {
			return productapi.BYOKListResult{}, service.mapError(encodeErr)
		}
		next = &encoded
		items = items[:100]
	}
	if err = insertSettingsAudit(ctx, tx, service.IDKey, query.RequestID, query.TenantID, query.UserID, query.SessionID, "byok_listed", "", service.now()); err != nil {
		return productapi.BYOKListResult{}, service.mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.BYOKListResult{}, service.mapError(err)
	}
	return productapi.BYOKListResult{Items: items, NextCursor: next}, nil
}

func (service BYOKService) Create(ctx context.Context, command productapi.BYOKCreateCommand) (productapi.BYOKCredentialResource, error) {
	host, valid := providerBoundHost(command.ProviderID, command.Endpoint)
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || !valid || !validStoredAPIKey(command.APIKey) {
		return productapi.BYOKCredentialResource{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(struct {
		RequestID  string `json:"request_id"`
		ProviderID string `json:"provider_id"`
		Endpoint   string `json:"endpoint"`
	}{command.ClientRequestID, command.ProviderID, command.Endpoint})
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+byokCreateOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-byok-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: byokCreateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.BYOKCredentialResource{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	current, found, err := service.observeScope(ctx, command.TenantID, command.UserID, command.SessionID, command.ProviderID, host)
	if err != nil {
		return productapi.BYOKCredentialResource{}, service.mapError(err)
	}
	credentialID, nextVersion := current.ID, uint64(1)
	if found {
		if current.Status == "active" || current.Status == "pending_validation" {
			return productapi.BYOKCredentialResource{}, productapi.ErrStateConflict
		}
		nextVersion = current.Version + 1
	} else {
		credentialID, err = ids.DeterministicUUID(service.IDKey, "byok-credential", recordID)
		if err != nil {
			return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
		}
	}
	secretRef := path.Join(service.SecretPrefix, command.TenantID, credentialID, "v"+strconv.FormatUint(nextVersion, 10))
	secretVersion, created, err := service.Vault.PutAPIKey(ctx, secretRef, command.APIKey)
	if err != nil {
		return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
	}
	secretVersionNumber, err := strconv.Atoi(secretVersion)
	if err != nil || secretVersionNumber < 1 {
		if created {
			_ = service.Vault.DestroySecretVersion(ctx, secretRef, 1)
		}
		return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
	}
	now := service.now()
	hint := "••••" + lastRunes(command.APIKey, 4)
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := requireSettingsSession(ctx, tx, command.TenantID, command.UserID, command.SessionID, now, true); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var locked storedBYOK
		innerErr := tx.QueryRow(ctx, `SELECT id::text,version,status FROM product.byok_credentials WHERE tenant_id=$1 AND user_id=$2 AND provider_id=$3 AND bound_host=$4 FOR UPDATE`, command.TenantID, command.UserID, command.ProviderID, host).Scan(&locked.ID, &locked.Version, &locked.Status)
		missing := errors.Is(innerErr, pgx.ErrNoRows)
		if innerErr != nil && !missing {
			return idempotency.Response{}, innerErr
		}
		if missing != !found || !missing && (locked.ID != current.ID || locked.Version != current.Version || locked.Status != current.Status) {
			return idempotency.Response{}, ErrRouteConflict
		}
		if _, innerErr = tx.Exec(ctx, `INSERT INTO product.byok_credential_versions(tenant_id,credential_id,version,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'active',$9,$9)`, command.TenantID, credentialID, nextVersion, command.UserID, command.ProviderID, host, secretRef, secretVersion, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if missing {
			_, innerErr = tx.Exec(ctx, `INSERT INTO product.byok_credentials(id,tenant_id,user_id,version,provider_id,bound_host,secret_ref,secret_version,secret_hint,status,last_validated_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'active',$10,$10,$10)`, credentialID, command.TenantID, command.UserID, nextVersion, command.ProviderID, host, secretRef, secretVersion, hint, now)
		} else {
			var tag pgconnCommandTag
			tag, innerErr = execTag(ctx, tx, `UPDATE product.byok_credentials SET version=$1,secret_ref=$2,secret_version=$3,secret_hint=$4,status='active',last_validated_at=$5,updated_at=$5 WHERE tenant_id=$6 AND id=$7 AND version=$8 AND status IN ('invalid','revoked')`, nextVersion, secretRef, secretVersion, hint, now, command.TenantID, credentialID, current.Version)
			if innerErr == nil && tag.RowsAffected() != 1 {
				innerErr = ErrRouteConflict
			}
		}
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.appendCredentialEvent(ctx, tx, command.CommandMetadata, credentialID, nextVersion, "ByokCredentialConfigured", recordID, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = insertSettingsAudit(ctx, tx, service.IDKey, command.RequestID, command.TenantID, command.UserID, command.SessionID, "byok_configured", credentialID, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.BYOKCredentialResource{ID: credentialID, Version: nextVersion, Status: "active", ProviderID: command.ProviderID, BoundHost: host, SecretHint: hint, LastValidatedAt: &now, UpdatedAt: now}
		return service.store(ctx, descriptor, result)
	})
	if err != nil {
		if created {
			_ = service.Vault.DestroySecretVersion(ctx, secretRef, secretVersionNumber)
		}
		return productapi.BYOKCredentialResource{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service BYOKService) Delete(ctx context.Context, command productapi.BYOKDeleteCommand) (productapi.BYOKCredentialResource, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.CredentialID == "" || command.ExpectedVersion < 1 {
		return productapi.BYOKCredentialResource{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(command)
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+byokDeleteOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-byok-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: byokDeleteOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.BYOKCredentialResource{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	current, err := service.observeCredential(ctx, command.TenantID, command.UserID, command.SessionID, command.CredentialID)
	if err != nil {
		return productapi.BYOKCredentialResource{}, service.mapError(err)
	}
	if current.Version != command.ExpectedVersion || current.Status == "revoked" {
		return productapi.BYOKCredentialResource{}, productapi.ErrStateConflict
	}
	secretVersion, err := strconv.Atoi(current.SecretVersion)
	if err != nil || secretVersion < 1 {
		return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
	}
	if err = service.Vault.DestroySecretVersion(ctx, current.SecretRef, secretVersion); err != nil {
		return productapi.BYOKCredentialResource{}, productapi.ErrDependencyUnavailable
	}
	now := service.now()
	next := current.Version + 1
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr := requireSettingsSession(ctx, tx, command.TenantID, command.UserID, command.SessionID, now, true); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var locked storedBYOK
		innerErr := tx.QueryRow(ctx, `SELECT version,status,provider_id,bound_host,secret_ref,secret_version,secret_hint,last_validated_at,updated_at FROM product.byok_credentials WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, command.TenantID, command.UserID, command.CredentialID).Scan(&locked.Version, &locked.Status, &locked.ProviderID, &locked.BoundHost, &locked.SecretRef, &locked.SecretVersion, &locked.SecretHint, &locked.LastValidatedAt, &locked.UpdatedAt)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if locked.Version != current.Version || locked.Status != current.Status || locked.SecretRef != current.SecretRef || locked.SecretVersion != current.SecretVersion {
			return idempotency.Response{}, ErrRouteConflict
		}
		if _, innerErr = tx.Exec(ctx, `INSERT INTO product.byok_credential_versions(tenant_id,credential_id,version,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'revoked',$9,$10)`, command.TenantID, command.CredentialID, next, command.UserID, current.ProviderID, current.BoundHost, current.SecretRef, current.SecretVersion, current.LastValidatedAt, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		tag, innerErr := tx.Exec(ctx, `UPDATE product.byok_credentials SET version=$1,status='revoked',updated_at=$2 WHERE tenant_id=$3 AND user_id=$4 AND id=$5 AND version=$6 AND status<>'revoked'`, next, now, command.TenantID, command.UserID, command.CredentialID, current.Version)
		if innerErr != nil || tag.RowsAffected() != 1 {
			if innerErr == nil {
				innerErr = ErrRouteConflict
			}
			return idempotency.Response{}, innerErr
		}
		if innerErr = service.appendCredentialEvent(ctx, tx, command.CommandMetadata, command.CredentialID, next, "ByokCredentialDeleted", recordID, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if innerErr = insertSettingsAudit(ctx, tx, service.IDKey, command.RequestID, command.TenantID, command.UserID, command.SessionID, "byok_deleted", command.CredentialID, now); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.BYOKCredentialResource{ID: command.CredentialID, Version: next, Status: "revoked", ProviderID: current.ProviderID, BoundHost: current.BoundHost, SecretHint: current.SecretHint, LastValidatedAt: current.LastValidatedAt, UpdatedAt: now}
		return service.store(ctx, descriptor, result)
	})
	if err != nil {
		return productapi.BYOKCredentialResource{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	result.Replayed = replayed && err == nil
	return result, service.mapError(err)
}

func (service BYOKService) observeScope(ctx context.Context, tenantID, userID, sessionID, providerID, host string) (storedBYOK, bool, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return storedBYOK{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return storedBYOK{}, false, err
	}
	if err = requireSettingsSession(ctx, tx, tenantID, userID, sessionID, service.now(), true); err != nil {
		return storedBYOK{}, false, err
	}
	var out storedBYOK
	err = tx.QueryRow(ctx, `SELECT id::text,version,status FROM product.byok_credentials WHERE tenant_id=$1 AND user_id=$2 AND provider_id=$3 AND bound_host=$4`, tenantID, userID, providerID, host).Scan(&out.ID, &out.Version, &out.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, false, err
	}
	return out, true, nil
}
func (service BYOKService) observeCredential(ctx context.Context, tenantID, userID, sessionID, id string) (storedBYOK, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return storedBYOK{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return storedBYOK{}, err
	}
	if err = requireSettingsSession(ctx, tx, tenantID, userID, sessionID, service.now(), true); err != nil {
		return storedBYOK{}, err
	}
	var out storedBYOK
	err = tx.QueryRow(ctx, `SELECT id::text,version,status,provider_id,bound_host,secret_ref,secret_version,secret_hint,last_validated_at,updated_at FROM product.byok_credentials WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, id).Scan(&out.ID, &out.Version, &out.Status, &out.ProviderID, &out.BoundHost, &out.SecretRef, &out.SecretVersion, &out.SecretHint, &out.LastValidatedAt, &out.UpdatedAt)
	if err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

func (service BYOKService) appendCredentialEvent(ctx context.Context, tx pgx.Tx, metadata productapi.CommandMetadata, id string, version uint64, eventType, seed string, now time.Time) error {
	eventID, _ := ids.DeterministicUUID(service.IDKey, "byok:event:"+eventType, seed)
	outboxID, _ := ids.DeterministicUUID(service.IDKey, "byok:outbox:"+eventType, seed)
	publishID, _ := ids.DeterministicUUID(service.IDKey, "byok:publish:"+eventType, seed)
	manifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: metadata.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": id, "subject_version": version})
	if err != nil {
		return err
	}
	_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: metadata.TenantID, UserID: metadata.UserID, EventType: eventType, SchemaVersion: 1, AggregateKind: "byok_credential", AggregateID: id, AggregateVersion: version, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(metadata.UserID, metadata.SessionID), CorrelationID: metadata.RequestID, PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}}})
	return err
}
func (service BYOKService) store(ctx context.Context, d payload.Descriptor, result productapi.BYOKCredentialResource) (idempotency.Response, error) {
	manifest, err := service.putJSON(ctx, d, result)
	if err != nil {
		return idempotency.Response{}, err
	}
	return idempotency.Response{Status: http.StatusOK, ContentType: "application/json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.Version}, nil
}
func (service BYOKService) read(ctx context.Context, d payload.Descriptor, response idempotency.Response) (productapi.BYOKCredentialResource, error) {
	encoded, err := service.Payloads.Get(ctx, d, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.BYOKCredentialResource{}, err
	}
	var result productapi.BYOKCredentialResource
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}
func (service BYOKService) putJSON(ctx context.Context, d payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, d, encoded)
}
func (service BYOKService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && service.Vault != nil && validSecretPrefix(service.SecretPrefix) && len(service.IDKey) >= 32 && len(service.CursorKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}
func (service BYOKService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (service BYOKService) mapError(err error) error {
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

var secretPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_/-]{0,127}$`)

func validSecretPrefix(value string) bool {
	return secretPrefixPattern.MatchString(value) && path.Clean(value) == value && !strings.Contains(value, "//") && !strings.HasSuffix(value, "/")
}
func validStoredAPIKey(value string) bool {
	return len(value) >= 8 && len(value) <= 16<<10 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
func providerBoundHost(provider, endpoint string) (string, bool) {
	managed := map[string]string{"openai": "api.openai.com", "anthropic": "api.anthropic.com"}
	if host := managed[provider]; host != "" {
		return host, endpoint == ""
	}
	if provider != "openai_compatible" || endpoint == "" {
		return "", false
	}
	parsed, err := url.Parse(endpoint)
	host := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(host)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" || host == "" || ip != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || host == "metadata.google.internal" {
		return "", false
	}
	return host, true
}
func lastRunes(value string, count int) string {
	runes := []rune(value)
	if len(runes) <= count {
		return string(runes)
	}
	return string(runes[len(runes)-count:])
}

func byokNullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

var _ productapi.BYOKService = BYOKService{}
