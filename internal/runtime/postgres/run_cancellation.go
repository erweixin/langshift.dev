package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const runtimeCancellationPayloadClass = "runtime-control-evidence"

// RunCancellationService turns an authoritative Run cancellation into
// Runtime termination requests. It never kills or releases a machine; the
// owning runtime-host-agent must still prove cleanup and complete termination.
type RunCancellationService struct {
	Store    Store
	Payloads payload.Store
}

type cancellationSession struct {
	identity  RecoveryIdentity
	status    string
	version   uint64
	allocated bool
}

func (service RunCancellationService) ConvergeRunCancellation(ctx context.Context, tenantID, runID, cancellationID, storeEpoch string) error {
	if !service.Store.valid() || service.Payloads == nil || tenantID == "" || runID == "" || cancellationID == "" || storeEpoch == "" {
		return ErrConfiguration
	}
	if storeEpoch != service.Store.StoreEpoch {
		return ErrStaleEpoch
	}
	if err := service.Store.requireEpoch(ctx); err != nil {
		return err
	}
	sessions, err := service.loadCancellationSessions(ctx, tenantID, runID, cancellationID, storeEpoch)
	if err != nil {
		return err
	}
	correlationID, err := ids.DeterministicUUID(service.Store.IDKey, "runtime-run-cancellation-correlation", cancellationID)
	if err != nil {
		return ErrConfiguration
	}
	actor := json.RawMessage(`{"kind":"service","id":"runtime-cancellation-converger"}`)
	for _, session := range sessions {
		at := service.Store.now()
		eventID, identifierErr := service.Store.deterministicID("runtime-lifecycle-event:RuntimeTerminationRequested", session.identity.SessionID, session.version+1)
		if identifierErr != nil {
			return identifierErr
		}
		encoded, marshalErr := json.Marshal(map[string]any{
			"subject_id": session.identity.SessionID, "subject_version": session.version + 1,
			"run_id": runID, "cancellation_id": cancellationID, "previous_state": session.status,
			"reason": "run_cancelled", "requested_at": at,
		})
		if marshalErr != nil {
			return marshalErr
		}
		manifest, putErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: eventID, Class: runtimeCancellationPayloadClass, ContentType: "application/json"}, encoded)
		if putErr != nil || manifest.Ref == "" || manifest.Hash == "" {
			return errors.Join(putErr, ErrConfiguration)
		}
		command := LifecycleCommand{
			TenantID: tenantID, SessionID: session.identity.SessionID, ExpectedVersion: session.version, ObservedAt: at,
			Payload: PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, Actor: actor, CorrelationID: correlationID,
		}
		if !session.allocated {
			if _, err = service.Store.RequestTermination(ctx, TerminationCommand{LifecycleCommand: command, Reason: "run_cancelled"}); err != nil {
				return err
			}
			continue
		}
		if _, err = service.Store.RequestRecoveryTermination(ctx, RecoveryTerminationCommand{
			LifecycleCommand: command, Authority: RecoveryCancellation, Identity: session.identity,
			RunID: runID, CancellationID: cancellationID, Reason: "run_cancelled",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (service RunCancellationService) loadCancellationSessions(ctx context.Context, tenantID, runID, cancellationID, storeEpoch string) ([]cancellationSession, error) {
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT s.tenant_id::text,s.id::text,s.status,s.version,
		a.id::text,s.provision_attempt_id::text,s.host_id,s.machine_id,s.guest_cid
		FROM agent.runtime_sessions s
		JOIN agent.runs r ON r.tenant_id=s.tenant_id AND r.id=s.run_id
		JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
		JOIN agent.events ce ON ce.tenant_id=c.tenant_id AND ce.id=c.request_event_id
		  AND ce.aggregate_kind='run_cancellation' AND ce.aggregate_id=c.id
		  AND ce.event_type='RunCancellationRequested' AND ce.aggregate_version=1
		LEFT JOIN agent.runtime_allocations a ON a.tenant_id=s.tenant_id AND a.session_id=s.id
		WHERE s.tenant_id=$1 AND s.run_id=$2 AND c.id=$3 AND c.store_epoch=$4 AND ce.store_epoch=$4
		  AND c.status='terminating' AND r.cancel_requested_at IS NOT NULL
		  AND ((s.status='requested' AND a.id IS NULL)
		    OR (s.status='provisioning' AND a.status='provisioning' AND a.session_version=s.version AND a.session_event_id=s.last_event_id)
		    OR (s.status IN ('ready','running','idle') AND a.status='active' AND a.session_version=s.version AND a.session_event_id=s.last_event_id))
		ORDER BY s.id`, tenantID, runID, cancellationID, storeEpoch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []cancellationSession
	for rows.Next() {
		var value cancellationSession
		var allocationID, provisionAttemptID, hostID, machineID sql.NullString
		var guestCID sql.NullInt64
		if err = rows.Scan(&value.identity.TenantID, &value.identity.SessionID, &value.status, &value.version,
			&allocationID, &provisionAttemptID, &hostID, &machineID, &guestCID); err != nil {
			return nil, err
		}
		value.allocated = allocationID.Valid
		if value.allocated {
			if !provisionAttemptID.Valid || !hostID.Valid || !machineID.Valid || !guestCID.Valid || guestCID.Int64 < 3 || guestCID.Int64 > int64(^uint32(0)) {
				return nil, ErrConfiguration
			}
			value.identity.AllocationID, value.identity.ProvisionAttemptID = allocationID.String, provisionAttemptID.String
			value.identity.HostID, value.identity.MachineID, value.identity.GuestCID = hostID.String, machineID.String, uint32(guestCID.Int64)
		}
		result = append(result, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
