package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	contractsapi "github.com/langshift/lites/internal/contracts/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type InternalUsageService struct {
	Pool     *pgxpool.Pool
	Billing  billingpostgres.Store
	Payloads payload.Store
	IDKey    []byte
	Now      func() time.Time
}

type internalReservation struct {
	ID, TenantID, UserID, BucketID, OperationKey, SubjectKind, SubjectID, Status string
	SubjectVersion, ReservedUnits, Version                                       uint64
	ExpiresAt                                                                    time.Time
}

func (service InternalUsageService) Reserve(ctx context.Context, command contractsapi.InternalUsageReserveCommand) (contractsapi.InternalUsageResult, error) {
	if !service.valid() || uuid.Validate(command.TenantID) != nil || uuid.Validate(command.UserID) != nil || uuid.Validate(command.SubjectID) != nil || command.RequestID == "" || command.IdempotencyKey == "" || command.WorkloadIdentity == "" || command.OperationKey == "" || command.RequestedUnits < 1 || command.SubjectVersion < 1 || command.SubjectKind != "provider_attempt" && command.SubjectKind != "tool_call" {
		return contractsapi.InternalUsageResult{}, contractsapi.ErrValidation
	}
	reservationID, err := ids.DeterministicUUID(service.IDKey, "internal-usage-reservation", command.TenantID+"\x00"+command.WorkloadIdentity+"\x00"+command.IdempotencyKey)
	if err != nil {
		return contractsapi.InternalUsageResult{}, err
	}
	bucketID, expiresAt, err := service.reserveScope(ctx, command, reservationID)
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	pointer, err := service.eventPayload(ctx, command.TenantID, reservationID, "reserved", map[string]any{
		"subject_id": command.SubjectID, "subject_version": command.SubjectVersion, "request_id": command.RequestID,
		"units": command.RequestedUnits, "cost_microunits": 0, "reservation_id": reservationID,
		"bucket_id": bucketID, "operation_key": command.OperationKey, "reserved_units": command.RequestedUnits, "expires_at": expiresAt,
	})
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	result, err := service.Billing.Reserve(ctx, billingpostgres.ReserveCommand{ReservationID: reservationID, RequestID: command.RequestID, TenantID: command.TenantID, UserID: command.UserID, BucketID: bucketID, OperationKey: command.OperationKey, SubjectKind: command.SubjectKind, SubjectID: command.SubjectID, SubjectVersion: command.SubjectVersion, ReservedUnits: command.RequestedUnits, ExpiresAt: expiresAt, CorrelationID: reservationID, Actor: workloadActor(command.WorkloadIdentity), ReservedEvent: pointer})
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	return contractsapi.InternalUsageResult{ReservationID: result.ReservationID, Status: result.Status, ReservedUnits: result.ReservedUnits, ExpiresAt: result.ExpiresAt, Replayed: result.Replayed}, nil
}

func (service InternalUsageService) Settle(ctx context.Context, command contractsapi.InternalUsageSettleCommand) (contractsapi.InternalUsageResult, error) {
	if !service.valid() || uuid.Validate(command.TenantID) != nil || uuid.Validate(command.ReservationID) != nil || uuid.Validate(command.ProviderAttemptID) != nil || command.RequestID == "" || command.IdempotencyKey == "" || command.WorkloadIdentity == "" || command.ExpectedVersion != 1 {
		return contractsapi.InternalUsageResult{}, contractsapi.ErrValidation
	}
	reservation, providerCost, err := service.loadReservation(ctx, command.TenantID, command.ReservationID, command.ProviderAttemptID)
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	if reservation.SubjectKind != "provider_attempt" || reservation.SubjectID != command.ProviderAttemptID || command.ActualUnits > reservation.ReservedUnits || providerCost != command.ProviderCostMicrounits || reservation.Version != 1 && !(reservation.Version == 2 && reservation.Status == "settled") {
		return contractsapi.InternalUsageResult{}, contractsapi.ErrStateConflict
	}
	_, ledgerID, err := service.Billing.TerminalIdentifiers("settled", reservation.ID)
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	pointer, err := service.eventPayload(ctx, command.TenantID, reservation.ID, "settled", map[string]any{
		"subject_id": reservation.ID, "subject_version": 2, "units": command.ActualUnits, "cost_microunits": command.ProviderCostMicrounits,
		"reservation_id": reservation.ID, "bucket_id": reservation.BucketID, "operation_key": reservation.OperationKey,
		"actual_units": command.ActualUnits, "ledger_entry_id": ledgerID, "provider_attempt_id": command.ProviderAttemptID,
	})
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	causationID, _ := ids.DeterministicUUID(service.IDKey, "internal-usage-settle-causation", command.TenantID+"\x00"+reservation.ID+"\x00"+command.IdempotencyKey)
	result, err := service.Billing.Settle(ctx, billingpostgres.SettleCommand{ReservationID: reservation.ID, TenantID: command.TenantID, ProviderAttemptID: command.ProviderAttemptID, ActualUnits: command.ActualUnits, CorrelationID: reservation.ID, CausationID: causationID, Actor: workloadActor(command.WorkloadIdentity), SettledEvent: pointer})
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	return contractsapi.InternalUsageResult{ReservationID: result.ReservationID, Status: result.Status, SettledUnits: result.ActualUnits, LedgerEntryID: result.LedgerEntryID, Replayed: result.Replayed}, nil
}

func (service InternalUsageService) Release(ctx context.Context, command contractsapi.InternalUsageReleaseCommand) (contractsapi.InternalUsageResult, error) {
	if !service.valid() || uuid.Validate(command.TenantID) != nil || uuid.Validate(command.ReservationID) != nil || command.RequestID == "" || command.IdempotencyKey == "" || command.WorkloadIdentity == "" || command.Reason == "" || command.ExpectedVersion != 1 {
		return contractsapi.InternalUsageResult{}, contractsapi.ErrValidation
	}
	reservation, _, err := service.loadReservation(ctx, command.TenantID, command.ReservationID, "")
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	if reservation.Version != 1 && !(reservation.Version == 2 && reservation.Status == "released") {
		return contractsapi.InternalUsageResult{}, contractsapi.ErrStateConflict
	}
	_, ledgerID, err := service.Billing.TerminalIdentifiers("released", reservation.ID)
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	pointer, err := service.eventPayload(ctx, command.TenantID, reservation.ID, "released", map[string]any{
		"subject_id": reservation.ID, "subject_version": 2, "units": reservation.ReservedUnits, "cost_microunits": 0,
		"reservation_id": reservation.ID, "bucket_id": reservation.BucketID, "operation_key": reservation.OperationKey,
		"released_units": reservation.ReservedUnits, "ledger_entry_id": ledgerID, "reason_code": command.Reason,
	})
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	causationID, _ := ids.DeterministicUUID(service.IDKey, "internal-usage-release-causation", command.TenantID+"\x00"+reservation.ID+"\x00"+command.IdempotencyKey)
	result, err := service.Billing.Release(ctx, billingpostgres.ReleaseCommand{ReservationID: reservation.ID, TenantID: command.TenantID, ReasonCode: command.Reason, CorrelationID: reservation.ID, CausationID: causationID, Actor: workloadActor(command.WorkloadIdentity), ReleasedEvent: pointer})
	if err != nil {
		return contractsapi.InternalUsageResult{}, service.mapError(err)
	}
	return contractsapi.InternalUsageResult{ReservationID: result.ReservationID, Status: result.Status, ReleasedUnits: result.ReleasedUnits, LedgerEntryID: result.LedgerEntryID, Replayed: result.Replayed}, nil
}

func (service InternalUsageService) reserveScope(ctx context.Context, command contractsapi.InternalUsageReserveCommand, reservationID string) (string, time.Time, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return "", time.Time{}, err
	}
	var bucketID string
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT bucket_id::text,expires_at FROM contracts.usage_reservations WHERE tenant_id=$1 AND (id=$2 OR operation_key=$3)`, command.TenantID, reservationID, command.OperationKey).Scan(&bucketID, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		now := service.now()
		var bucketExpiresAt time.Time
		err = tx.QueryRow(ctx, `SELECT id::text,expires_at FROM contracts.credit_buckets WHERE tenant_id=$1 AND starts_at<=$2 AND expires_at>$2 AND granted_units-reserved_units-settled_units>=$3 ORDER BY expires_at,id LIMIT 1`, command.TenantID, now, command.RequestedUnits).Scan(&bucketID, &bucketExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", time.Time{}, billingpostgres.ErrInsufficientCredits
		}
		expiresAt = now.Add(15 * time.Minute)
		if bucketExpiresAt.Before(expiresAt) {
			expiresAt = bucketExpiresAt
		}
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", time.Time{}, err
	}
	return bucketID, expiresAt, nil
}

func (service InternalUsageService) loadReservation(ctx context.Context, tenantID, reservationID, providerAttemptID string) (internalReservation, uint64, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return internalReservation{}, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return internalReservation{}, 0, err
	}
	var result internalReservation
	err = tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,user_id::text,bucket_id::text,operation_key,subject_kind,subject_id::text,subject_version,reserved_units,status,version,expires_at FROM contracts.usage_reservations WHERE tenant_id=$1 AND id=$2`, tenantID, reservationID).Scan(&result.ID, &result.TenantID, &result.UserID, &result.BucketID, &result.OperationKey, &result.SubjectKind, &result.SubjectID, &result.SubjectVersion, &result.ReservedUnits, &result.Status, &result.Version, &result.ExpiresAt)
	if err != nil {
		return internalReservation{}, 0, err
	}
	var providerCost uint64
	if providerAttemptID != "" {
		err = tx.QueryRow(ctx, `SELECT cost_microunits FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 AND usage_reservation_id=$3`, tenantID, providerAttemptID, reservationID).Scan(&providerCost)
		if err != nil {
			return internalReservation{}, 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return internalReservation{}, 0, err
	}
	return result, providerCost, nil
}

func (service InternalUsageService) eventPayload(ctx context.Context, tenantID, reservationID, state string, value map[string]any) (billingpostgres.PayloadPointer, error) {
	payloadID, err := ids.DeterministicUUID(service.IDKey, "internal-usage-event-payload:"+state, reservationID)
	if err != nil {
		return billingpostgres.PayloadPointer{}, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return billingpostgres.PayloadPointer{}, err
	}
	manifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: payloadID, Class: "event-payload", ContentType: "application/json"}, encoded)
	return billingpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (service InternalUsageService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && service.Billing.Compatible(service.Pool, service.Billing.StoreEpoch)
}
func (service InternalUsageService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (service InternalUsageService) mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, billingpostgres.ErrInvalidCommand):
		return contractsapi.ErrValidation
	case errors.Is(err, pgx.ErrNoRows):
		return contractsapi.ErrResourceNotFound
	case errors.Is(err, billingpostgres.ErrInsufficientCredits), errors.Is(err, billingpostgres.ErrReservationConflict), errors.Is(err, billingpostgres.ErrUnsafeRelease):
		return contractsapi.ErrStateConflict
	default:
		return err
	}
}

func workloadActor(identity string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"kind": "service", "workload_identity": identity})
	return encoded
}

var _ contractsapi.InternalUsageService = InternalUsageService{}
