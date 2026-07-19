package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type ReserveCommand struct {
	ReservationID, RequestID, TenantID, UserID string
	BucketID, OperationKey                     string
	SubjectKind, SubjectID                     string
	SubjectVersion, ReservedUnits              uint64
	ExpiresAt                                  time.Time
	CorrelationID                              string
	Actor                                      json.RawMessage
	ReservedEvent                              PayloadPointer
}

type Reservation struct {
	ReservationID, BucketID, OperationKey, Status                 string
	ProviderAttemptID, TerminalEventID, LedgerEntryID, ReasonCode string
	ReservedUnits, ActualUnits, ReleasedUnits                     uint64
	Version                                                       uint64
	ExpiresAt, UpdatedAt                                          time.Time
	Replayed                                                      bool
}

func (store Store) Reserve(ctx context.Context, command ReserveCommand) (Reservation, error) {
	if !store.Valid() || !validReserve(command) {
		return Reservation{}, ErrInvalidCommand
	}
	if err := store.RequireEpoch(ctx); err != nil {
		return Reservation{}, err
	}
	command.ExpiresAt = command.ExpiresAt.UTC().Truncate(time.Microsecond)
	now := store.now()
	if !command.ExpiresAt.After(now) {
		return Reservation{}, ErrInvalidCommand
	}
	identifier, err := store.identifiers("reserved", command.ReservationID, 1)
	if err != nil {
		return Reservation{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Reservation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return Reservation{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, command.TenantID+":"+command.OperationKey); err != nil {
		return Reservation{}, err
	}
	var replay Reservation
	var userID, requestID, subjectKind, subjectID, reservedEvent string
	var subjectVersion uint64
	err = tx.QueryRow(ctx, `SELECT id::text,user_id::text,request_id,bucket_id::text,operation_key,subject_kind,subject_id::text,subject_version,reserved_units,status,version,expires_at,updated_at,reserved_event_id::text,COALESCE(actual_units,0),COALESCE(released_units,0) FROM contracts.usage_reservations WHERE tenant_id=$1 AND (id=$2 OR operation_key=$3 OR (user_id=$4 AND request_id=$5)) FOR UPDATE`, command.TenantID, command.ReservationID, command.OperationKey, command.UserID, command.RequestID).Scan(&replay.ReservationID, &userID, &requestID, &replay.BucketID, &replay.OperationKey, &subjectKind, &subjectID, &subjectVersion, &replay.ReservedUnits, &replay.Status, &replay.Version, &replay.ExpiresAt, &replay.UpdatedAt, &reservedEvent, &replay.ActualUnits, &replay.ReleasedUnits)
	if err == nil {
		if replay.ReservationID != command.ReservationID || userID != command.UserID || requestID != command.RequestID || replay.BucketID != command.BucketID || replay.OperationKey != command.OperationKey || subjectKind != command.SubjectKind || subjectID != command.SubjectID || subjectVersion != command.SubjectVersion || replay.ReservedUnits != command.ReservedUnits || !replay.ExpiresAt.Equal(command.ExpiresAt) || reservedEvent != identifier.event {
			return Reservation{}, ErrReservationConflict
		}
		replay.Replayed = true
		if err = tx.Commit(ctx); err != nil {
			return Reservation{}, err
		}
		return replay, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, err
	}
	var bucketVersion uint64
	err = tx.QueryRow(ctx, `SELECT contracts.reserve_credit_units($1,$2,$3,$4,$5)`, command.TenantID, command.BucketID, command.ReservedUnits, now, command.ExpiresAt).Scan(&bucketVersion)
	if err != nil {
		return Reservation{}, err
	}
	if bucketVersion == 0 {
		return Reservation{}, ErrInsufficientCredits
	}
	if _, err = tx.Exec(ctx, `INSERT INTO contracts.usage_reservations(id,tenant_id,user_id,bucket_id,operation_key,reserved_units,status,expires_at,request_id,subject_kind,subject_id,subject_version,reserved_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'reserved',$7,$8,$9,$10,$11,$12,$13,$13)`, command.ReservationID, command.TenantID, command.UserID, command.BucketID, command.OperationKey, command.ReservedUnits, command.ExpiresAt, command.RequestID, command.SubjectKind, command.SubjectID, command.SubjectVersion, identifier.event, now); err != nil {
		return Reservation{}, ErrReservationConflict
	}
	event := usageEvent(identifier, eventpostgres.Event{TenantID: command.TenantID, UserID: command.UserID, EventType: "UsageReserved", SchemaVersion: 1, AggregateKind: "usage_reservation", AggregateID: command.ReservationID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.ReservedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return Reservation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Reservation{}, err
	}
	return Reservation{ReservationID: command.ReservationID, BucketID: command.BucketID, OperationKey: command.OperationKey, Status: "reserved", ReservedUnits: command.ReservedUnits, Version: 1, ExpiresAt: command.ExpiresAt, UpdatedAt: now}, nil
}

func validReserve(command ReserveCommand) bool {
	return command.ReservationID != "" && command.RequestID != "" && command.TenantID != "" && command.UserID != "" && command.BucketID != "" && command.OperationKey != "" && (command.SubjectKind == "provider_attempt" || command.SubjectKind == "tool_call") && command.SubjectID != "" && command.SubjectVersion > 0 && command.ReservedUnits > 0 && command.CorrelationID != "" && validActor(command.Actor) && validPointer(command.ReservedEvent)
}
