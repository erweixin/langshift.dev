package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
	"io"
	"net/http"
	"time"
)

const preferencesUpdateOperation = "preferences.update.v2"

type PreferencesService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                       string
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

func (s PreferencesService) Get(ctx context.Context, q productapi.PreferencesQuery) (productapi.PreferencesResource, error) {
	if !s.valid() || q.TenantID == "" || q.UserID == "" {
		return productapi.PreferencesResource{}, productapi.ErrValidation
	}
	id, e := ids.DeterministicUUID(s.IDKey, "user-preferences", q.TenantID+"\x00"+q.UserID)
	if e != nil {
		return productapi.PreferencesResource{}, productapi.ErrDependencyUnavailable
	}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if e != nil {
		return productapi.PreferencesResource{}, s.mapError(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, q.TenantID); e != nil {
		return productapi.PreferencesResource{}, s.mapError(e)
	}
	out := productapi.PreferencesResource{ID: id, Status: "active"}
	var coach json.RawMessage
	e = tx.QueryRow(ctx, `SELECT version,locale,timezone,coach_preferences,updated_at FROM product.user_preferences WHERE tenant_id=$1 AND user_id=$2`, q.TenantID, q.UserID).Scan(&out.Version, &out.Locale, &out.Timezone, &coach, &out.UpdatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		out.Version = 1
		out.Locale = "en"
		out.Timezone = "UTC"
		out.CoachPreferences = defaultCoachPreferences()
		out.UpdatedAt = time.Unix(0, 0).UTC()
		return out, nil
	}
	if e != nil {
		return out, s.mapError(e)
	}
	if e = json.Unmarshal(coach, &out.CoachPreferences); e != nil || !out.CoachPreferences.Valid() {
		return out, s.mapError(payload.ErrIntegrity)
	}
	if e = tx.Commit(ctx); e != nil {
		return out, s.mapError(e)
	}
	return out, nil
}
func (s PreferencesService) Update(ctx context.Context, c productapi.UpdatePreferencesCommand) (productapi.PreferencesResource, error) {
	if !s.valid() || !validMissionMetadata(c.CommandMetadata) || c.Locale != "en" && c.Locale != "zh-CN" || c.ExpectedVersion < 1 || !c.CoachPreferences.Valid() {
		return productapi.PreferencesResource{}, productapi.ErrValidation
	}
	if _, e := time.LoadLocation(c.Timezone); e != nil {
		return productapi.PreferencesResource{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(c)
	requestHash, e := idempotency.RequestDigest(canonical, s.RequestDigestPepper)
	if e != nil {
		return productapi.PreferencesResource{}, productapi.ErrDependencyUnavailable
	}
	recordID, e := ids.DeterministicUUID(s.IDKey, "product-idempotency:"+preferencesUpdateOperation, c.TenantID+"\x00"+c.UserID+"\x00"+c.IdempotencyKey)
	if e != nil {
		return productapi.PreferencesResource{}, productapi.ErrDependencyUnavailable
	}
	preferencesID, _ := ids.DeterministicUUID(s.IDKey, "user-preferences", c.TenantID+"\x00"+c.UserID)
	descriptor := payload.Descriptor{TenantID: c.TenantID, ObjectID: recordID, Class: "product-preferences-idempotency", ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: c.TenantID, UserID: c.UserID, OperationID: preferencesUpdateOperation}, RawKey: c.IdempotencyKey, RequestHash: requestHash, RequestID: c.RequestID}
	executor := idempotencypostgres.Executor{Pool: s.Pool, KeyPepper: s.IdempotencyKeyPepper, TTL: s.IdempotencyTTL, Now: s.Now}
	if response, found, er := executor.LoadCompleted(ctx, input); er != nil {
		return productapi.PreferencesResource{}, s.mapError(er)
	} else if found {
		out, re := s.read(ctx, descriptor, response)
		out.Replayed = re == nil
		return out, s.mapError(re)
	}
	coach, _ := json.Marshal(c.CoachPreferences)
	now := s.now()
	response, replayed, e := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, er := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, c.TenantID); er != nil {
			return idempotency.Response{}, er
		}
		var current uint64
		var previousLocale, previousTimezone string
		var previousCoach json.RawMessage
		er := tx.QueryRow(ctx, `SELECT version,locale,timezone,coach_preferences FROM product.user_preferences WHERE tenant_id=$1 AND user_id=$2 FOR UPDATE`, c.TenantID, c.UserID).Scan(&current, &previousLocale, &previousTimezone, &previousCoach)
		missing := errors.Is(er, pgx.ErrNoRows)
		if er != nil && !missing {
			return idempotency.Response{}, er
		}
		if missing {
			current = 1
			previousLocale, previousTimezone = "en", "UTC"
			previousCoach, _ = json.Marshal(defaultCoachPreferences())
		}
		if current != c.ExpectedVersion {
			return idempotency.Response{}, ErrRouteConflict
		}
		next := current + 1
		if missing {
			_, er = tx.Exec(ctx, `INSERT INTO product.user_preferences(tenant_id,user_id,version,locale,timezone,coach_preferences,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, c.TenantID, c.UserID, next, c.Locale, c.Timezone, coach, now)
		} else {
			tag, x := tx.Exec(ctx, `UPDATE product.user_preferences SET version=$1,locale=$2,timezone=$3,coach_preferences=$4,updated_at=$5 WHERE tenant_id=$6 AND user_id=$7 AND version=$8`, next, c.Locale, c.Timezone, coach, now, c.TenantID, c.UserID, current)
			er = x
			if er == nil && tag.RowsAffected() != 1 {
				er = ErrRouteConflict
			}
		}
		if er != nil {
			return idempotency.Response{}, er
		}
		var lastEventVersion uint64
		if er = tx.QueryRow(ctx, `SELECT COALESCE(max(aggregate_version),0) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='user_preferences' AND aggregate_id=$2`, c.TenantID, preferencesID).Scan(&lastEventVersion); er != nil {
			return idempotency.Response{}, er
		}
		if lastEventVersion == 0 {
			if current != 1 {
				return idempotency.Response{}, ErrRouteConflict
			}
			initEvent, _ := ids.DeterministicUUID(s.IDKey, "preferences-initialized-event", preferencesID)
			initOutbox, _ := ids.DeterministicUUID(s.IDKey, "preferences-initialized-outbox", preferencesID)
			initPublish, _ := ids.DeterministicUUID(s.IDKey, "preferences-initialized-publish", preferencesID)
			var previous productapi.CoachPreferences
			if json.Unmarshal(previousCoach, &previous) != nil || !previous.Valid() {
				return idempotency.Response{}, payload.ErrIntegrity
			}
			initPayload, x := s.putJSON(ctx, payload.Descriptor{TenantID: c.TenantID, ObjectID: initEvent, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": preferencesID, "subject_version": 1, "locale": previousLocale, "timezone": previousTimezone, "coach_preferences": previous})
			if x != nil {
				return idempotency.Response{}, x
			}
			_, x = s.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: initEvent, TenantID: c.TenantID, UserID: c.UserID, EventType: "PreferencesInitialized", SchemaVersion: 1, AggregateKind: "user_preferences", AggregateID: preferencesID, AggregateVersion: 1, StoreEpoch: s.StoreEpoch, OccurredAt: now, Actor: missionActor(c.UserID, c.SessionID), CorrelationID: c.RequestID, PayloadRef: initPayload.Ref, PayloadHash: initPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: initOutbox, CommandID: initPublish, CommandType: "events.publish", PayloadRef: initPayload.Ref, PayloadHash: initPayload.Hash}}})
			if x != nil {
				return idempotency.Response{}, x
			}
		} else if lastEventVersion != current {
			return idempotency.Response{}, ErrRouteConflict
		}
		eventID, _ := ids.DeterministicUUID(s.IDKey, "preferences-updated-event", recordID)
		eventPayload, er := s.putJSON(ctx, payload.Descriptor{TenantID: c.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": preferencesID, "subject_version": next, "locale": c.Locale, "timezone": c.Timezone, "coach_preferences": c.CoachPreferences})
		if er != nil {
			return idempotency.Response{}, er
		}
		outbox, _ := ids.DeterministicUUID(s.IDKey, "preferences-updated-outbox", recordID)
		publish, _ := ids.DeterministicUUID(s.IDKey, "preferences-updated-publish", recordID)
		_, er = s.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: c.TenantID, UserID: c.UserID, EventType: "PreferencesUpdated", SchemaVersion: 1, AggregateKind: "user_preferences", AggregateID: preferencesID, AggregateVersion: next, StoreEpoch: s.StoreEpoch, OccurredAt: now, Actor: missionActor(c.UserID, c.SessionID), CorrelationID: c.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outbox, CommandID: publish, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
		if er != nil {
			return idempotency.Response{}, er
		}
		out := productapi.PreferencesResource{ID: preferencesID, Version: next, Status: "active", Locale: c.Locale, Timezone: c.Timezone, CoachPreferences: c.CoachPreferences, UpdatedAt: now}
		stored, er := s.putJSON(ctx, descriptor, out)
		if er != nil {
			return idempotency.Response{}, er
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.preferences.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: next}, nil
	})
	if e != nil {
		return productapi.PreferencesResource{}, s.mapError(e)
	}
	out, e := s.read(ctx, descriptor, response)
	if e != nil {
		return out, s.mapError(e)
	}
	out.Replayed = replayed
	return out, nil
}
func defaultCoachPreferences() productapi.CoachPreferences {
	return productapi.CoachPreferences{SchemaVersion: 1, Difficulty: "standard", AvailableMinutes: 45, Tone: "encouraging", ExplanationDepth: "balanced"}
}
func (s PreferencesService) putJSON(ctx context.Context, d payload.Descriptor, v any) (payload.Manifest, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return payload.Manifest{}, e
	}
	return s.Payloads.Put(ctx, d, b)
}
func (s PreferencesService) read(ctx context.Context, d payload.Descriptor, r idempotency.Response) (productapi.PreferencesResource, error) {
	b, e := s.Payloads.Get(ctx, d, payload.Manifest{Ref: r.PayloadRef, Hash: r.Hash})
	if e != nil {
		return productapi.PreferencesResource{}, e
	}
	var out productapi.PreferencesResource
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || !errors.Is(dec.Decode(&struct{}{}), io.EOF) {
		return out, payload.ErrIntegrity
	}
	return out, nil
}
func (s PreferencesService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (s PreferencesService) valid() bool {
	return s.Pool != nil && s.Payloads != nil && len(s.IDKey) >= 32 && len(s.IdempotencyKeyPepper) >= 32 && len(s.RequestDigestPepper) >= 32 && s.StoreEpoch != "" && s.IdempotencyTTL > 0
}
func (s PreferencesService) mapError(e error) error {
	switch {
	case e == nil:
		return nil
	case errors.Is(e, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(e, idempotency.ErrKeyConflict), errors.Is(e, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(e, ErrRouteConflict):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, e)
	}
}

var _ productapi.PreferencesService = PreferencesService{}
