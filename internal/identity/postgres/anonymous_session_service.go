package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

var ErrAnonymousTenantUnavailable = errors.New("anonymous system tenant is unavailable")

type AnonymousSessionService struct {
	Pool           *pgxpool.Pool
	SystemTenantID string
	Signer         anonymoussession.Signer
	Payloads       payload.Store
	Appender       eventpostgres.Appender
	StoreEpoch     string
	TTL            time.Duration
	Random         io.Reader
	Now            func() time.Time
}

type AnonymousBootstrap struct {
	Credential anonymoussession.Credential
	Principal  anonymoussession.Principal
}

type anonymousBootstrapPrepared struct {
	Bootstrap                    AnonymousBootstrap
	EventID, OutboxID, CommandID string
	EventPayload                 payload.Manifest
}

func (service AnonymousSessionService) Create(ctx context.Context) (AnonymousBootstrap, error) {
	if service.Pool == nil || service.SystemTenantID == "" || service.Random == nil || service.Payloads == nil || service.StoreEpoch == "" || service.TTL <= 0 || service.TTL > anonymoussession.MaximumTTL {
		return AnonymousBootstrap{}, anonymoussession.ErrInvalidHandle
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	for range 3 {
		prepared, err := service.prepare(ctx, now)
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		err = service.commitPrepared(ctx, tx, prepared, now)
		if err == nil {
			err = tx.Commit(ctx)
			if err != nil {
				_ = tx.Rollback(ctx)
			}
		} else {
			_ = tx.Rollback(ctx)
		}
		if err == nil {
			return prepared.Bootstrap, nil
		}
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
			return AnonymousBootstrap{}, err
		}
	}
	return AnonymousBootstrap{}, errors.New("anonymous credential collision retry exhausted")
}

func (service AnonymousSessionService) prepare(ctx context.Context, now time.Time) (anonymousBootstrapPrepared, error) {
	credential, err := service.Signer.New(now, service.TTL)
	if err != nil {
		return anonymousBootstrapPrepared{}, err
	}
	values := make([]string, 5)
	for index := range values {
		values[index], err = ids.NewUUIDFrom(service.Random)
		if err != nil {
			return anonymousBootstrapPrepared{}, err
		}
	}
	subjectID, ephemeralUserID, eventID, outboxID, commandID := values[0], values[1], values[2], values[3], values[4]
	encodedPayload, err := json.Marshal(map[string]any{"subject_id": subjectID, "subject_version": 1})
	if err != nil {
		return anonymousBootstrapPrepared{}, err
	}
	eventPayload, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: service.SystemTenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, encodedPayload)
	if err != nil {
		return anonymousBootstrapPrepared{}, err
	}
	bootstrap := AnonymousBootstrap{Credential: credential, Principal: anonymoussession.Principal{AnonymousSubjectID: subjectID, UserID: ephemeralUserID, TenantID: service.SystemTenantID, ExpiresAt: credential.ExpiresAt}}
	return anonymousBootstrapPrepared{Bootstrap: bootstrap, EventID: eventID, OutboxID: outboxID, CommandID: commandID, EventPayload: eventPayload}, nil
}

func (service AnonymousSessionService) commitPrepared(ctx context.Context, tx pgx.Tx, prepared anonymousBootstrapPrepared, now time.Time) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, service.SystemTenantID); err != nil {
		return err
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT kind='anonymous_system' AND status='active' FROM identity.tenants WHERE id=$1`, service.SystemTenantID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return ErrAnonymousTenantUnavailable
	}
	if err != nil {
		return err
	}
	bootstrap := prepared.Bootstrap
	if _, err = tx.Exec(ctx, `INSERT INTO identity.anonymous_subjects(id,anonymous_subject_hash,ephemeral_user_id,system_tenant_id,expires_at) VALUES($1,$2,$3,$4,$5)`, bootstrap.Principal.AnonymousSubjectID, bootstrap.Credential.Digest[:], bootstrap.Principal.UserID, service.SystemTenantID, bootstrap.Credential.ExpiresAt); err != nil {
		return err
	}
	_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: prepared.EventID, TenantID: service.SystemTenantID, UserID: bootstrap.Principal.UserID, EventType: "AnonymousSubjectCreated", SchemaVersion: 1, AggregateKind: "anonymous_subject", AggregateID: bootstrap.Principal.AnonymousSubjectID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: json.RawMessage(`{"kind":"system","id":"identity-service"}`), CorrelationID: prepared.EventID, PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: prepared.OutboxID, CommandID: prepared.CommandID, CommandType: "events.publish", PayloadRef: prepared.EventPayload.Ref, PayloadHash: prepared.EventPayload.Hash}}})
	return err
}
