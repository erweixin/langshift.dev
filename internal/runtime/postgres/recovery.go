package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type RecoveryAuthority string

const (
	RecoveryOwned        RecoveryAuthority = "owned_machine"
	RecoveryDeadline     RecoveryAuthority = "deadline"
	RecoveryCancellation RecoveryAuthority = "run_cancellation"
)

type RecoveryIdentity struct {
	TenantID, SessionID, AllocationID, ProvisionAttemptID string
	HostID, MachineID                                     string
	GuestCID                                              uint32
}

type RecoveryState struct {
	RecoveryIdentity
	SessionVersion, AllocationVersion, ProvisionFence   uint64
	SessionStatus, AllocationStatus                     string
	ProvisionLeaseExpiresAt, IdleDeadline, KillDeadline sql.NullTime
	ExecutionDeadline                                   time.Time
}

type RecoveryTerminationCommand struct {
	LifecycleCommand
	Authority       RecoveryAuthority
	Identity        RecoveryIdentity
	HostControlHash []byte
	RunID           string
	CancellationID  string
	Reason          string
}

type RecoveryTerminatedCommand struct {
	LifecycleCommand
	Identity           RecoveryIdentity
	HostControlHash    []byte
	CleanupReceiptHash string
	UsageManifest      json.RawMessage
}

func (store Store) InspectOwnedMachine(ctx context.Context, identity RecoveryIdentity, hostControlHash []byte) (RecoveryState, error) {
	if !store.valid() || !validRecoveryIdentity(identity) || len(hostControlHash) != 32 {
		return RecoveryState{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return RecoveryState{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RecoveryState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	state, err := store.lockOwnedMachine(ctx, tx, identity, hostControlHash)
	if err != nil {
		return RecoveryState{}, mapRecoveryAuthorityError(err)
	}
	return state, tx.Commit(ctx)
}

func (store Store) ListHostMachines(ctx context.Context, hostID string, hostControlHash []byte, afterSession string, limit int) ([]RecoveryState, error) {
	if !store.valid() || hostID == "" || len(hostControlHash) != 32 || limit < 1 || limit > 5000 {
		return nil, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return nil, err
	}
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id,session_id,allocation_id,provision_attempt_id,machine_id,guest_cid,
	  session_version,session_status,allocation_status,provision_fence
	FROM agent.runtime_list_host_machines($1,$2,$3,NULLIF($4,'')::uuid,$5)`, store.StoreEpoch, hostID, hostControlHash, afterSession, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RecoveryState, 0, limit)
	for rows.Next() {
		var state RecoveryState
		var guestCID int64
		state.HostID = hostID
		if err = rows.Scan(&state.TenantID, &state.SessionID, &state.AllocationID, &state.ProvisionAttemptID, &state.MachineID, &guestCID,
			&state.SessionVersion, &state.SessionStatus, &state.AllocationStatus, &state.ProvisionFence); err != nil || guestCID < 3 || guestCID > int64(^uint32(0)) {
			return nil, errors.Join(err, ErrConfiguration)
		}
		state.GuestCID = uint32(guestCID)
		result = append(result, state)
	}
	return result, rows.Err()
}

func (store Store) ListRecoveryTenants(ctx context.Context, after string, limit, shardIndex, shardCount int, at time.Time) ([]string, error) {
	if !store.valid() || limit < 1 || limit > 5000 || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount || at.IsZero() {
		return nil, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return nil, err
	}
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_runtime_recovery_tenants($1,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, store.StoreEpoch, after, limit, shardIndex, shardCount, at.UTC().Truncate(time.Microsecond))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var tenant string
		if err = rows.Scan(&tenant); err != nil {
			return nil, err
		}
		result = append(result, tenant)
	}
	return result, rows.Err()
}

func (store Store) ListDueSessions(ctx context.Context, tenantID, afterSession string, limit int, at time.Time) ([]RecoveryState, error) {
	if !store.valid() || tenantID == "" || limit < 1 || limit > 1000 || at.IsZero() {
		return nil, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return nil, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT s.tenant_id::text,s.id::text,a.id::text,s.provision_attempt_id::text,s.host_id,s.machine_id,s.guest_cid,
	  s.version,s.status,a.version,a.status,s.provision_fence,s.provision_lease_expires_at,s.idle_deadline,s.execution_deadline,s.kill_deadline
	FROM agent.runtime_sessions s JOIN agent.runtime_allocations a ON a.tenant_id=s.tenant_id AND a.session_id=s.id
	JOIN agent.events e ON e.tenant_id=s.tenant_id AND e.id=s.last_event_id AND e.store_epoch=$4
	WHERE s.tenant_id=$1 AND s.id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
	  AND ((s.status='provisioning' AND s.provision_lease_expires_at<=$3)
	    OR (s.status IN ('ready','idle') AND s.idle_deadline<=$3)
	    OR (s.status='running' AND s.execution_deadline<=$3))
	ORDER BY s.id LIMIT $5`, tenantID, afterSession, at.UTC().Truncate(time.Microsecond), store.StoreEpoch, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RecoveryState, 0, limit)
	for rows.Next() {
		state, scanErr := scanRecoveryState(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, state)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (store Store) RequestRecoveryTermination(ctx context.Context, command RecoveryTerminationCommand) (LifecycleResult, error) {
	if !validLifecycleCommandWithoutLease(command.LifecycleCommand) || command.LeaseToken != "" || command.Reason == "" || len(command.Reason) > 128 || !validRecoveryIdentity(command.Identity) || command.Identity.TenantID != command.TenantID || command.Identity.SessionID != command.SessionID ||
		(command.Authority == RecoveryOwned && (len(command.HostControlHash) != 32 || command.RunID != "" || command.CancellationID != "")) ||
		(command.Authority == RecoveryDeadline && (len(command.HostControlHash) != 0 || command.RunID != "" || command.CancellationID != "")) ||
		(command.Authority == RecoveryCancellation && (len(command.HostControlHash) != 0 || command.RunID == "" || command.CancellationID == "")) ||
		command.Authority != RecoveryOwned && command.Authority != RecoveryDeadline && command.Authority != RecoveryCancellation {
		return LifecycleResult{}, ErrInvalidCommand
	}
	return store.withRecoveryLifecycle(ctx, command.LifecycleCommand, command.Authority, command.Identity, command.HostControlHash, command.RunID, command.CancellationID, "RuntimeTerminationRequested", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, now time.Time, eventID string) (LifecycleResult, error) {
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		killDeadline := occurredAt.Add(session.KillGrace)
		if session.Status == "termination_requested" && session.Version == nextVersion && session.TerminationRequestedAt.Valid && session.TerminationRequestedAt.Time.Equal(occurredAt) && session.KillDeadline.Valid && session.KillDeadline.Time.Equal(killDeadline) && session.TerminationReason.Valid && session.TerminationReason.String == command.Reason && allocation != nil && allocation.Status == "releasing" && allocation.SessionVersion == nextVersion && allocation.SessionEventID == eventID {
			if ok, err := store.lifecycleEventMatches(ctx, tx, session, eventID, nextVersion, "RuntimeTerminationRequested", command.Payload); err != nil || !ok {
				return LifecycleResult{}, replayError(err)
			}
			return LifecycleResult{SessionID: session.ID, Status: "termination_requested", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: killDeadline, Replayed: true}, nil
		}
		if session.Version != command.ExpectedVersion || !terminationSource(session.Status) || session.Status == "requested" || allocation == nil || allocation.Status != "provisioning" && allocation.Status != "active" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !validObservedAt(occurredAt, session.UpdatedAt, now) || !killDeadline.After(occurredAt) {
			return LifecycleResult{}, ErrSessionConflict
		}
		if err := store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeTerminationRequested", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return LifecycleResult{}, err
		}
		if err := settleRunningExecutionUnknown(ctx, tx, session, eventID, nextVersion, occurredAt, command.Payload, command.Reason); err != nil {
			return LifecycleResult{}, err
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='termination_requested',provision_lease_hash=NULL,provision_lease_expires_at=NULL,termination_requested_at=$2,kill_deadline=$3,termination_reason=$4,last_event_id=$5,updated_at=$2 WHERE tenant_id=$6 AND id=$7 AND version=$8 AND status=$9`, nextVersion, occurredAt, killDeadline, command.Reason, eventID, session.TenantID, session.ID, session.Version, session.Status); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_allocations SET version=version+1,status='releasing',session_version=$1,session_event_id=$2,lease_expires_at=$3,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status=$8`, nextVersion, eventID, killDeadline, occurredAt, session.TenantID, allocation.ID, allocation.Version, allocation.Status); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		return LifecycleResult{SessionID: session.ID, Status: "termination_requested", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: killDeadline}, nil
	})
}

func (store Store) CompleteRecoveryTermination(ctx context.Context, command RecoveryTerminatedCommand) (LifecycleResult, error) {
	if !validLifecycleCommandWithoutLease(command.LifecycleCommand) || command.LeaseToken != "" || !validRecoveryIdentity(command.Identity) || command.Identity.TenantID != command.TenantID || command.Identity.SessionID != command.SessionID || len(command.HostControlHash) != 32 || !runtimeReceiptPattern.MatchString(command.CleanupReceiptHash) || !validUsageManifest(command.UsageManifest) {
		return LifecycleResult{}, ErrInvalidCommand
	}
	return store.withRecoveryLifecycle(ctx, command.LifecycleCommand, RecoveryOwned, command.Identity, command.HostControlHash, "", "", "RuntimeSessionTerminated", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, now time.Time, eventID string) (LifecycleResult, error) {
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		if session.Status == "terminated" && session.Version == nextVersion && session.TerminatedAt.Valid && session.TerminatedAt.Time.Equal(occurredAt) && session.CleanupReceiptHash.Valid && session.CleanupReceiptHash.String == command.CleanupReceiptHash && jsonEqual(session.UsageManifest, command.UsageManifest) && allocationTerminalReplay(allocation, nextVersion, eventID) {
			if ok, err := store.lifecycleEventMatches(ctx, tx, session, eventID, nextVersion, "RuntimeSessionTerminated", command.Payload); err != nil || !ok {
				return LifecycleResult{}, replayError(err)
			}
			return LifecycleResult{SessionID: session.ID, Status: "terminated", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Replayed: true}, nil
		}
		if session.Version != command.ExpectedVersion || session.Status != "termination_requested" || allocation == nil || allocation.Status != "releasing" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !validObservedAt(occurredAt, session.UpdatedAt, now) {
			return LifecycleResult{}, ErrSessionConflict
		}
		if err := store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeSessionTerminated", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return LifecycleResult{}, err
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='terminated',terminated_at=$2,cleanup_receipt_hash=$3,usage_manifest=$4,last_event_id=$5,updated_at=$2 WHERE tenant_id=$6 AND id=$7 AND version=$8 AND status='termination_requested'`, nextVersion, occurredAt, command.CleanupReceiptHash, command.UsageManifest, eventID, session.TenantID, session.ID, session.Version); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_allocations SET version=version+1,status='released',session_version=$1,session_event_id=$2,lease_token_hash=NULL,lease_expires_at=NULL,terminal_reason=$3,terminal_receipt_hash=$4,released_at=$5,updated_at=$5 WHERE tenant_id=$6 AND id=$7 AND version=$8 AND status='releasing'`, nextVersion, eventID, session.TerminationReason.String, command.CleanupReceiptHash, occurredAt, session.TenantID, allocation.ID, allocation.Version); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		return LifecycleResult{SessionID: session.ID, Status: "terminated", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt}, nil
	})
}

type recoveryMutation func(pgx.Tx, lifecycleSession, *lifecycleAllocation, time.Time, string) (LifecycleResult, error)

func (store Store) withRecoveryLifecycle(ctx context.Context, command LifecycleCommand, authority RecoveryAuthority, identity RecoveryIdentity, hostControlHash []byte, runID, cancellationID, eventType string, mutation recoveryMutation) (LifecycleResult, error) {
	if !store.valid() {
		return LifecycleResult{}, ErrConfiguration
	}
	if err := store.requireEpoch(ctx); err != nil {
		return LifecycleResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LifecycleResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return LifecycleResult{}, err
	}
	var locked RecoveryState
	if authority == RecoveryOwned {
		locked, err = store.lockOwnedMachine(ctx, tx, identity, hostControlHash)
	} else if authority == RecoveryDeadline {
		locked, err = store.lockDueSession(ctx, tx, identity, command.ExpectedVersion, store.now())
	} else {
		locked, err = store.lockCancelledRunSession(ctx, tx, identity, runID, cancellationID, command.ExpectedVersion, store.now())
	}
	if err != nil || !sameRecoveryIdentity(locked.RecoveryIdentity, identity) {
		return LifecycleResult{}, errors.Join(err, ErrSessionConflict)
	}
	session, err := loadLifecycleSession(ctx, tx, command.TenantID, command.SessionID, store.StoreEpoch)
	if err != nil {
		return LifecycleResult{}, err
	}
	allocation, err := loadLifecycleAllocation(ctx, tx, command.TenantID, command.SessionID)
	if err != nil || allocation == nil || session.Version != locked.SessionVersion || allocation.Version != locked.AllocationVersion {
		return LifecycleResult{}, errors.Join(err, ErrSessionConflict)
	}
	eventID, err := store.deterministicID("runtime-lifecycle-event:"+eventType, session.ID, command.ExpectedVersion+1)
	if err != nil {
		return LifecycleResult{}, err
	}
	result, err := mutation(tx, session, allocation, store.now(), eventID)
	if err != nil {
		return LifecycleResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return LifecycleResult{}, err
	}
	return result, nil
}

func (store Store) lockOwnedMachine(ctx context.Context, tx pgx.Tx, identity RecoveryIdentity, hostControlHash []byte) (RecoveryState, error) {
	row := tx.QueryRow(ctx, `SELECT * FROM agent.runtime_lock_owned_machine($1,$2,$3,$4,$5,$6,$7,$8,$9)`, store.StoreEpoch, identity.HostID, hostControlHash, identity.TenantID, identity.SessionID, identity.AllocationID, identity.ProvisionAttemptID, identity.MachineID, identity.GuestCID)
	state, err := scanRecoveryState(row)
	return state, mapRecoveryAuthorityError(err)
}

func (store Store) lockDueSession(ctx context.Context, tx pgx.Tx, identity RecoveryIdentity, expectedVersion uint64, at time.Time) (RecoveryState, error) {
	row := tx.QueryRow(ctx, `SELECT * FROM agent.runtime_lock_due_session($1,$2,$3,$4,$5)`, store.StoreEpoch, identity.TenantID, identity.SessionID, expectedVersion, at)
	state, err := scanRecoveryState(row)
	return state, mapRecoveryAuthorityError(err)
}

func (store Store) lockCancelledRunSession(ctx context.Context, tx pgx.Tx, identity RecoveryIdentity, runID, cancellationID string, expectedVersion uint64, at time.Time) (RecoveryState, error) {
	row := tx.QueryRow(ctx, `SELECT * FROM agent.runtime_lock_cancelled_run_session($1,$2,$3,$4,$5,$6,$7)`, store.StoreEpoch, identity.TenantID, runID, cancellationID, identity.SessionID, expectedVersion, at)
	state, err := scanRecoveryState(row)
	return state, mapRecoveryAuthorityError(err)
}

type recoveryScanner interface{ Scan(...any) error }

func scanRecoveryState(row recoveryScanner) (RecoveryState, error) {
	var state RecoveryState
	var guestCID int64
	err := row.Scan(&state.TenantID, &state.SessionID, &state.AllocationID, &state.ProvisionAttemptID, &state.HostID, &state.MachineID,
		&guestCID, &state.SessionVersion, &state.SessionStatus, &state.AllocationVersion, &state.AllocationStatus, &state.ProvisionFence,
		&state.ProvisionLeaseExpiresAt, &state.IdleDeadline, &state.ExecutionDeadline, &state.KillDeadline)
	if err != nil || guestCID < 3 || guestCID > int64(^uint32(0)) {
		return RecoveryState{}, errors.Join(err, ErrSessionConflict)
	}
	state.GuestCID = uint32(guestCID)
	return state, nil
}

func validRecoveryIdentity(identity RecoveryIdentity) bool {
	return identity.TenantID != "" && identity.SessionID != "" && identity.AllocationID != "" && identity.ProvisionAttemptID != "" && identity.HostID != "" && identity.MachineID != "" && identity.GuestCID >= 3
}

func sameRecoveryIdentity(left, right RecoveryIdentity) bool {
	return left == right
}

func mapRecoveryAuthorityError(err error) error {
	if err == nil {
		return nil
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "40001" && (strings.Contains(databaseError.Message, "owned runtime machine does not match") || strings.Contains(databaseError.Message, "not due for recovery") || strings.Contains(databaseError.Message, "not authorized by run cancellation")) {
		return errors.Join(ErrSessionConflict, err)
	}
	return err
}

func (state RecoveryState) Due(at time.Time) bool {
	at = at.UTC()
	switch state.SessionStatus {
	case "provisioning":
		return state.ProvisionLeaseExpiresAt.Valid && !state.ProvisionLeaseExpiresAt.Time.After(at)
	case "ready", "idle":
		return state.IdleDeadline.Valid && !state.IdleDeadline.Time.After(at)
	case "running":
		return !state.ExecutionDeadline.After(at)
	case "termination_requested":
		return state.KillDeadline.Valid && !state.KillDeadline.Time.After(at)
	default:
		return false
	}
}
