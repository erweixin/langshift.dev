package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type SettleCommand struct {
	ReservationID, TenantID, ProviderAttemptID string
	ActualUnits                                uint64
	CorrelationID, CausationID                 string
	Actor                                      json.RawMessage
	SettledEvent                               PayloadPointer
}

type ReleaseCommand struct {
	ReservationID, TenantID, ReasonCode, CorrelationID, CausationID string
	Actor                                                           json.RawMessage
	ReleasedEvent                                                   PayloadPointer
}

func (store Store) Release(ctx context.Context, command ReleaseCommand) (Reservation, error) {
	if !store.Valid() {
		return Reservation{}, ErrConfiguration
	}
	if err := store.RequireEpoch(ctx); err != nil {
		return Reservation{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Reservation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.ReleaseInTx(ctx, tx, command)
	if err != nil {
		return Reservation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Reservation{}, err
	}
	return result, nil
}

func (store Store) SettleInTx(ctx context.Context, tx pgx.Tx, command SettleCommand) (Reservation, error) {
	if tx == nil || !store.Valid() || command.ReservationID == "" || command.TenantID == "" || command.ProviderAttemptID == "" || command.CorrelationID == "" || command.CausationID == "" || !validActor(command.Actor) || !validPointer(command.SettledEvent) {
		return Reservation{}, ErrInvalidCommand
	}
	identifier, err := store.identifiers("settled", command.ReservationID, 2)
	if err != nil {
		return Reservation{}, ErrConfiguration
	}
	now := store.now()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return Reservation{}, err
	}
	reservation, userID, subjectKind, subjectID, err := lockReservation(ctx, tx, command.TenantID, command.ReservationID)
	if err != nil {
		return Reservation{}, err
	}
	if reservation.Status != "reserved" {
		if reservation.Status == "settled" && reservation.Version == 2 && reservation.ActualUnits == command.ActualUnits && reservation.ProviderAttemptID == command.ProviderAttemptID && reservation.TerminalEventID == identifier.event {
			reservation.Replayed = true
			return reservation, nil
		}
		return Reservation{}, ErrReservationConflict
	}
	if subjectKind != "provider_attempt" || subjectID != command.ProviderAttemptID || command.ActualUnits > reservation.ReservedUnits {
		return Reservation{}, ErrReservationConflict
	}
	var providerStatus, providerUsageStatus string
	var providerVersion uint64
	if err = tx.QueryRow(ctx, `SELECT status,version,usage_status FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 AND usage_reservation_id=$3 FOR SHARE`, command.TenantID, command.ProviderAttemptID, command.ReservationID).Scan(&providerStatus, &providerVersion, &providerUsageStatus); err != nil {
		return Reservation{}, ErrReservationConflict
	}
	providerTerminal := (providerVersion == 3 && (providerStatus == "completed" || providerStatus == "failed" || providerStatus == "cancelled") && (providerUsageStatus == "confirmed" || providerUsageStatus == "estimated")) || (providerVersion == 4 && providerStatus == "outcome_unknown" && providerUsageStatus == "confirmed")
	if !providerTerminal {
		return Reservation{}, ErrReservationConflict
	}
	var bucketVersion uint64
	if err = tx.QueryRow(ctx, `SELECT contracts.settle_credit_units($1,$2,$3,$4,$5)`, command.TenantID, reservation.BucketID, reservation.ReservedUnits, command.ActualUnits, now).Scan(&bucketVersion); err != nil {
		return Reservation{}, err
	}
	if bucketVersion == 0 {
		return Reservation{}, ErrReservationConflict
	}
	released := reservation.ReservedUnits - command.ActualUnits
	tag, err := tx.Exec(ctx, `UPDATE contracts.usage_reservations SET version=2,status='settled',settled_at=$1,terminal_event_id=$2,ledger_entry_id=$3,actual_units=$4,released_units=$5,provider_attempt_id=$6,updated_at=$1 WHERE tenant_id=$7 AND id=$8 AND status='reserved' AND version=1`, now, identifier.event, identifier.ledger, command.ActualUnits, released, command.ProviderAttemptID, command.TenantID, command.ReservationID)
	if err != nil || tag.RowsAffected() != 1 {
		return Reservation{}, ErrReservationConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO contracts.usage_ledger(id,tenant_id,user_id,reservation_id,entry_kind,units,operation_key,causation_id,recorded_at,bucket_id,reservation_version,event_id) VALUES($1,$2,$3,$4,'settlement',$5,$6,$7,$8,$9,2,$10)`, identifier.ledger, command.TenantID, userID, command.ReservationID, command.ActualUnits, reservation.OperationKey, command.CausationID, now, reservation.BucketID, identifier.event); err != nil {
		return Reservation{}, ErrReservationConflict
	}
	event := usageEvent(identifier, eventpostgres.Event{TenantID: command.TenantID, UserID: userID, EventType: "UsageSettled", SchemaVersion: 1, AggregateKind: "usage_reservation", AggregateID: command.ReservationID, AggregateVersion: 2, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &command.CausationID, CorrelationID: command.CorrelationID}, command.SettledEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return Reservation{}, err
	}
	reservation.Status, reservation.Version, reservation.ActualUnits, reservation.ReleasedUnits, reservation.UpdatedAt = "settled", 2, command.ActualUnits, released, now
	reservation.ProviderAttemptID, reservation.TerminalEventID = command.ProviderAttemptID, identifier.event
	return reservation, nil
}

func (store Store) ReleaseInTx(ctx context.Context, tx pgx.Tx, command ReleaseCommand) (Reservation, error) {
	if tx == nil || !store.Valid() || command.ReservationID == "" || command.TenantID == "" || command.ReasonCode == "" || command.CorrelationID == "" || command.CausationID == "" || !validActor(command.Actor) || !validPointer(command.ReleasedEvent) {
		return Reservation{}, ErrInvalidCommand
	}
	identifier, err := store.identifiers("released", command.ReservationID, 2)
	if err != nil {
		return Reservation{}, ErrConfiguration
	}
	now := store.now()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return Reservation{}, err
	}
	reservation, userID, subjectKind, subjectID, err := lockReservation(ctx, tx, command.TenantID, command.ReservationID)
	if err != nil {
		return Reservation{}, err
	}
	if reservation.Status != "reserved" {
		if reservation.Status == "released" && reservation.Version == 2 && reservation.TerminalEventID == identifier.event && reservation.ReasonCode == command.ReasonCode {
			reservation.Replayed = true
			return reservation, nil
		}
		return Reservation{}, ErrReservationConflict
	}
	if subjectKind == "provider_attempt" {
		var status string
		err = tx.QueryRow(ctx, `SELECT status FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 AND usage_reservation_id=$3`, command.TenantID, subjectID, command.ReservationID).Scan(&status)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) || err == nil && status != "abandoned" {
			return Reservation{}, ErrUnsafeRelease
		}
	}
	var bucketVersion uint64
	if err = tx.QueryRow(ctx, `SELECT contracts.release_credit_units($1,$2,$3,$4)`, command.TenantID, reservation.BucketID, reservation.ReservedUnits, now).Scan(&bucketVersion); err != nil {
		return Reservation{}, err
	}
	if bucketVersion == 0 {
		return Reservation{}, ErrReservationConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE contracts.usage_reservations SET version=2,status='released',released_at=$1,terminal_event_id=$2,ledger_entry_id=$3,actual_units=0,released_units=reserved_units,reason_code=$4,updated_at=$1 WHERE tenant_id=$5 AND id=$6 AND status='reserved' AND version=1`, now, identifier.event, identifier.ledger, command.ReasonCode, command.TenantID, command.ReservationID)
	if err != nil || tag.RowsAffected() != 1 {
		return Reservation{}, ErrReservationConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO contracts.usage_ledger(id,tenant_id,user_id,reservation_id,entry_kind,units,operation_key,causation_id,recorded_at,bucket_id,reservation_version,event_id) VALUES($1,$2,$3,$4,'release',$5,$6,$7,$8,$9,2,$10)`, identifier.ledger, command.TenantID, userID, command.ReservationID, reservation.ReservedUnits, reservation.OperationKey, command.CausationID, now, reservation.BucketID, identifier.event); err != nil {
		return Reservation{}, ErrReservationConflict
	}
	event := usageEvent(identifier, eventpostgres.Event{TenantID: command.TenantID, UserID: userID, EventType: "UsageReleased", SchemaVersion: 1, AggregateKind: "usage_reservation", AggregateID: command.ReservationID, AggregateVersion: 2, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &command.CausationID, CorrelationID: command.CorrelationID}, command.ReleasedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return Reservation{}, err
	}
	reservation.Status, reservation.Version, reservation.ActualUnits, reservation.ReleasedUnits, reservation.UpdatedAt = "released", 2, 0, reservation.ReservedUnits, now
	reservation.TerminalEventID, reservation.ReasonCode = identifier.event, command.ReasonCode
	return reservation, nil
}

func lockReservation(ctx context.Context, tx pgx.Tx, tenantID, reservationID string) (Reservation, string, string, string, error) {
	var reservation Reservation
	var userID, subjectKind, subjectID string
	var actual, released *uint64
	var providerAttemptID, terminalEventID, reasonCode *string
	err := tx.QueryRow(ctx, `SELECT id::text,user_id::text,bucket_id::text,operation_key,subject_kind,subject_id::text,reserved_units,status,version,expires_at,updated_at,actual_units,released_units,provider_attempt_id::text,terminal_event_id::text,reason_code FROM contracts.usage_reservations WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, reservationID).Scan(&reservation.ReservationID, &userID, &reservation.BucketID, &reservation.OperationKey, &subjectKind, &subjectID, &reservation.ReservedUnits, &reservation.Status, &reservation.Version, &reservation.ExpiresAt, &reservation.UpdatedAt, &actual, &released, &providerAttemptID, &terminalEventID, &reasonCode)
	if err != nil {
		return Reservation{}, "", "", "", ErrReservationConflict
	}
	if actual != nil {
		reservation.ActualUnits = *actual
	}
	if released != nil {
		reservation.ReleasedUnits = *released
	}
	if providerAttemptID != nil {
		reservation.ProviderAttemptID = *providerAttemptID
	}
	if terminalEventID != nil {
		reservation.TerminalEventID = *terminalEventID
	}
	if reasonCode != nil {
		reservation.ReasonCode = *reasonCode
	}
	return reservation, userID, subjectKind, subjectID, nil
}
