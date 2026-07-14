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

func (service AnonymousSessionService) Create(ctx context.Context) (AnonymousBootstrap, error) {
	if service.Pool == nil || service.SystemTenantID == "" || service.Random == nil || service.Payloads == nil || service.StoreEpoch == "" || service.TTL <= 0 || service.TTL > anonymoussession.MaximumTTL {
		return AnonymousBootstrap{}, anonymoussession.ErrInvalidHandle
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	for range 3 {
		credential, err := service.Signer.New(now, service.TTL)
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		subjectID, err := ids.NewUUIDFrom(service.Random)
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		ephemeralUserID, err := ids.NewUUIDFrom(service.Random)
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		eventID, err := ids.NewUUIDFrom(service.Random)
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		outboxID, err := ids.NewUUIDFrom(service.Random)
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		commandID, err := ids.NewUUIDFrom(service.Random)
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		encodedPayload, err := json.Marshal(map[string]any{"subject_id": subjectID, "subject_version": 1})
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		eventPayload, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: service.SystemTenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, encodedPayload)
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			return AnonymousBootstrap{}, err
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, service.SystemTenantID); err == nil {
			var active bool
			err = tx.QueryRow(ctx, `SELECT kind='anonymous_system' AND status='active' FROM identity.tenants WHERE id=$1`, service.SystemTenantID).Scan(&active)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
				err = ErrAnonymousTenantUnavailable
			}
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO identity.anonymous_subjects(id,anonymous_subject_hash,ephemeral_user_id,system_tenant_id,expires_at) VALUES($1,$2,$3,$4,$5)`, subjectID, credential.Digest[:], ephemeralUserID, service.SystemTenantID, credential.ExpiresAt)
		}
		if err == nil {
			_, err = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: service.SystemTenantID, UserID: ephemeralUserID, EventType: "AnonymousSubjectCreated", SchemaVersion: 1, AggregateKind: "anonymous_subject", AggregateID: subjectID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: json.RawMessage(`{"kind":"system","id":"identity-service"}`), CorrelationID: eventID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: commandID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
		}
		if err == nil {
			err = tx.Commit(ctx)
			if err != nil {
				_ = tx.Rollback(ctx)
			}
		} else {
			_ = tx.Rollback(ctx)
		}
		if err == nil {
			return AnonymousBootstrap{Credential: credential, Principal: anonymoussession.Principal{AnonymousSubjectID: subjectID, UserID: ephemeralUserID, TenantID: service.SystemTenantID, ExpiresAt: credential.ExpiresAt}}, nil
		}
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
			return AnonymousBootstrap{}, err
		}
	}
	return AnonymousBootstrap{}, errors.New("anonymous credential collision retry exhausted")
}
