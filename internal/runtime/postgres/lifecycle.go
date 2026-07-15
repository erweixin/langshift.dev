package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

var runtimeReceiptPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type LifecycleCommand struct {
	TenantID, SessionID, ProvisionAttemptID string
	ProvisionFence, ExpectedVersion         uint64
	LeaseToken                              string
	ObservedAt                              time.Time
	Payload                                 PayloadPointer
	Actor                                   json.RawMessage
	CorrelationID                           string
}

type ReadyCommand struct {
	LifecycleCommand
	BootReceiptHash string
}

type TerminationCommand struct {
	LifecycleCommand
	Reason string
}

type TerminatedCommand struct {
	LifecycleCommand
	CleanupReceiptHash string
	UsageManifest      json.RawMessage
}

type LifecycleResult struct {
	SessionID  string    `json:"session_id"`
	Status     string    `json:"status"`
	EventID    string    `json:"event_id"`
	Version    uint64    `json:"version"`
	OccurredAt time.Time `json:"occurred_at"`
	Deadline   time.Time `json:"deadline"`
	Replayed   bool      `json:"replayed"`
}

type lifecycleSession struct {
	ID, TenantID, UserID, Status, LastEventID, ProvisionAttemptID string
	Version, ProvisionFence                                       uint64
	HostID                                                        sql.NullString
	ProvisioningAt, ReadyAt, RunningAt, LastActivityAt            sql.NullTime
	IdleDeadline, TerminationRequestedAt, KillDeadline            sql.NullTime
	TerminationReason                                             sql.NullString
	BootReceiptHash                                               sql.NullString
	TerminatedAt                                                  sql.NullTime
	CleanupReceiptHash                                            sql.NullString
	UsageManifest                                                 []byte
	ExecutionDeadline, UpdatedAt                                  time.Time
	IdleTimeout, KillGrace                                        time.Duration
}

type lifecycleAllocation struct {
	ID, Status, SessionEventID string
	Version, SessionVersion    uint64
	LeaseHash                  []byte
	LeaseExpiresAt             sql.NullTime
}

func (store Store) MarkReady(ctx context.Context, command ReadyCommand) (LifecycleResult, error) {
	if !validLifecycleCommand(command.LifecycleCommand) || command.ExpectedVersion != 2 || !runtimeReceiptPattern.MatchString(command.BootReceiptHash) {
		return LifecycleResult{}, ErrInvalidCommand
	}
	return store.withLifecycle(ctx, command.LifecycleCommand, "RuntimeSessionReady", "ready", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, digest []byte, now time.Time, eventID string) (LifecycleResult, error) {
		occurredAt := command.ObservedAt.UTC().Truncate(time.Microsecond)
		idleDeadline := occurredAt.Add(session.IdleTimeout)
		if idleDeadline.After(session.ExecutionDeadline) {
			idleDeadline = session.ExecutionDeadline
		}
		if session.Status == "ready" && session.Version == 3 && session.ReadyAt.Valid && session.ReadyAt.Time.Equal(occurredAt) && session.IdleDeadline.Valid && session.IdleDeadline.Time.Equal(idleDeadline) && session.BootReceiptHash.Valid && session.BootReceiptHash.String == command.BootReceiptHash && allocation != nil && allocation.Status == "active" && allocation.SessionVersion == 3 && allocation.SessionEventID == eventID && validAllocationAuthority(session, allocation, command.LifecycleCommand, digest, occurredAt) {
			if ok, err := store.lifecycleEventMatches(ctx, tx, session, eventID, 3, "RuntimeSessionReady", command.Payload); err != nil || !ok {
				return LifecycleResult{}, replayError(err)
			}
			return LifecycleResult{SessionID: session.ID, Status: "ready", EventID: eventID, Version: 3, OccurredAt: occurredAt, Deadline: idleDeadline, Replayed: true}, nil
		}
		if session.Status != "provisioning" || session.Version != 2 || allocation == nil || allocation.Status != "provisioning" || allocation.SessionVersion != 2 || allocation.SessionEventID != session.LastEventID || session.ProvisionAttemptID != command.ProvisionAttemptID || session.ProvisionFence != command.ProvisionFence || !bytes.Equal(allocation.LeaseHash, digest) {
			return LifecycleResult{}, ErrSessionConflict
		}
		if !validObservedAt(occurredAt, session.UpdatedAt, now) || !session.ExecutionDeadline.After(occurredAt) || !idleDeadline.After(occurredAt) {
			return LifecycleResult{}, ErrInvalidCommand
		}
		if err := store.appendLifecycleEvent(ctx, tx, session, eventID, 3, "RuntimeSessionReady", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return LifecycleResult{}, err
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=3,status='ready',provision_lease_hash=NULL,provision_lease_expires_at=NULL,ready_at=$1,idle_deadline=$2,boot_receipt_hash=$3,last_event_id=$4,updated_at=$1 WHERE tenant_id=$5 AND id=$6 AND version=2 AND status='provisioning'`, occurredAt, idleDeadline, command.BootReceiptHash, eventID, session.TenantID, session.ID); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_allocations SET version=version+1,status='active',session_version=3,session_event_id=$1,lease_expires_at=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status='provisioning'`, eventID, session.ExecutionDeadline, occurredAt, session.TenantID, allocation.ID, allocation.Version); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		return LifecycleResult{SessionID: session.ID, Status: "ready", EventID: eventID, Version: 3, OccurredAt: occurredAt, Deadline: idleDeadline}, nil
	})
}

func (store Store) MarkRunning(ctx context.Context, command LifecycleCommand) (LifecycleResult, error) {
	if !validLifecycleCommand(command) || command.ExpectedVersion < 3 {
		return LifecycleResult{}, ErrInvalidCommand
	}
	return store.withLifecycle(ctx, command, "RuntimeSessionExecutionStarted", "running", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, digest []byte, now time.Time, eventID string) (LifecycleResult, error) {
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		if session.Status == "running" && session.Version == nextVersion && session.LastActivityAt.Valid && session.LastActivityAt.Time.Equal(occurredAt) && allocation != nil && allocation.Status == "active" && allocation.SessionVersion == nextVersion && allocation.SessionEventID == eventID && validAllocationAuthority(session, allocation, command, digest, occurredAt) {
			if ok, err := store.lifecycleEventMatches(ctx, tx, session, eventID, nextVersion, "RuntimeSessionExecutionStarted", command.Payload); err != nil || !ok {
				return LifecycleResult{}, replayError(err)
			}
			return LifecycleResult{SessionID: session.ID, Status: "running", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: session.ExecutionDeadline, Replayed: true}, nil
		}
		if session.Version != command.ExpectedVersion || session.Status != "ready" && session.Status != "idle" || allocation == nil || allocation.Status != "active" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !validAllocationAuthority(session, allocation, command, digest, occurredAt) {
			return LifecycleResult{}, ErrSessionConflict
		}
		if !validObservedAt(occurredAt, session.UpdatedAt, now) || !session.ExecutionDeadline.After(occurredAt) {
			return LifecycleResult{}, ErrInvalidCommand
		}
		if err := store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeSessionExecutionStarted", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return LifecycleResult{}, err
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='running',running_at=COALESCE(running_at,$2),last_activity_at=$2,idle_deadline=NULL,last_event_id=$3,updated_at=$2 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status=$7`, nextVersion, occurredAt, eventID, session.TenantID, session.ID, session.Version, session.Status); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		if err := syncActiveAllocation(ctx, tx, session, allocation, eventID, nextVersion, occurredAt); err != nil {
			return LifecycleResult{}, err
		}
		return LifecycleResult{SessionID: session.ID, Status: "running", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: session.ExecutionDeadline}, nil
	})
}

func (store Store) MarkIdle(ctx context.Context, command LifecycleCommand) (LifecycleResult, error) {
	if !validLifecycleCommand(command) || command.ExpectedVersion < 4 {
		return LifecycleResult{}, ErrInvalidCommand
	}
	return store.withLifecycle(ctx, command, "RuntimeSessionIdle", "idle", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, digest []byte, now time.Time, eventID string) (LifecycleResult, error) {
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		idleDeadline := occurredAt.Add(session.IdleTimeout)
		if idleDeadline.After(session.ExecutionDeadline) {
			idleDeadline = session.ExecutionDeadline
		}
		if session.Status == "idle" && session.Version == nextVersion && session.LastActivityAt.Valid && session.LastActivityAt.Time.Equal(occurredAt) && session.IdleDeadline.Valid && session.IdleDeadline.Time.Equal(idleDeadline) && allocation != nil && allocation.Status == "active" && allocation.SessionVersion == nextVersion && allocation.SessionEventID == eventID && validAllocationAuthority(session, allocation, command, digest, occurredAt) {
			if ok, err := store.lifecycleEventMatches(ctx, tx, session, eventID, nextVersion, "RuntimeSessionIdle", command.Payload); err != nil || !ok {
				return LifecycleResult{}, replayError(err)
			}
			return LifecycleResult{SessionID: session.ID, Status: "idle", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: idleDeadline, Replayed: true}, nil
		}
		if session.Version != command.ExpectedVersion || session.Status != "running" || allocation == nil || allocation.Status != "active" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !validAllocationAuthority(session, allocation, command, digest, occurredAt) {
			return LifecycleResult{}, ErrSessionConflict
		}
		if !validObservedAt(occurredAt, session.UpdatedAt, now) || !idleDeadline.After(occurredAt) {
			return LifecycleResult{}, ErrInvalidCommand
		}
		if err := store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeSessionIdle", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return LifecycleResult{}, err
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='idle',last_activity_at=$2,idle_deadline=$3,last_event_id=$4,updated_at=$2 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status='running'`, nextVersion, occurredAt, idleDeadline, eventID, session.TenantID, session.ID, session.Version); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		if err := syncActiveAllocation(ctx, tx, session, allocation, eventID, nextVersion, occurredAt); err != nil {
			return LifecycleResult{}, err
		}
		return LifecycleResult{SessionID: session.ID, Status: "idle", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: idleDeadline}, nil
	})
}

func (store Store) RequestTermination(ctx context.Context, command TerminationCommand) (LifecycleResult, error) {
	if !validLifecycleCommandWithoutLease(command.LifecycleCommand) || command.Reason == "" || len(command.Reason) > 128 {
		return LifecycleResult{}, ErrInvalidCommand
	}
	return store.withLifecycle(ctx, command.LifecycleCommand, "RuntimeTerminationRequested", "termination_requested", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, digest []byte, now time.Time, eventID string) (LifecycleResult, error) {
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		killDeadline := occurredAt.Add(session.KillGrace)
		if session.Status == "termination_requested" && session.Version == nextVersion && session.TerminationRequestedAt.Valid && session.TerminationRequestedAt.Time.Equal(occurredAt) && session.KillDeadline.Valid && session.KillDeadline.Time.Equal(killDeadline) && session.TerminationReason.Valid && session.TerminationReason.String == command.Reason && terminationAllocationReplay(session, allocation, command.LifecycleCommand, digest, nextVersion, eventID) {
			if ok, err := store.lifecycleEventMatches(ctx, tx, session, eventID, nextVersion, "RuntimeTerminationRequested", command.Payload); err != nil || !ok {
				return LifecycleResult{}, replayError(err)
			}
			return LifecycleResult{SessionID: session.ID, Status: "termination_requested", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: killDeadline, Replayed: true}, nil
		}
		if session.Version != command.ExpectedVersion || !terminationSource(session.Status) || !validObservedAt(occurredAt, session.UpdatedAt, now) || !killDeadline.After(occurredAt) {
			return LifecycleResult{}, ErrSessionConflict
		}
		if allocation == nil {
			if session.Status != "requested" || command.LeaseToken != "" {
				return LifecycleResult{}, ErrSessionConflict
			}
		} else if allocation.Status != "provisioning" && allocation.Status != "active" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !validAllocationIdentity(session, allocation, command.LifecycleCommand, digest) {
			return LifecycleResult{}, ErrSessionConflict
		}
		if err := store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeTerminationRequested", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return LifecycleResult{}, err
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='termination_requested',provision_lease_hash=NULL,provision_lease_expires_at=NULL,termination_requested_at=$2,kill_deadline=$3,termination_reason=$4,last_event_id=$5,updated_at=$2 WHERE tenant_id=$6 AND id=$7 AND version=$8 AND status=$9`, nextVersion, occurredAt, killDeadline, command.Reason, eventID, session.TenantID, session.ID, session.Version, session.Status); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		if allocation != nil {
			if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_allocations SET version=version+1,status='releasing',session_version=$1,session_event_id=$2,lease_expires_at=$3,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status=$8`, nextVersion, eventID, killDeadline, occurredAt, session.TenantID, allocation.ID, allocation.Version, allocation.Status); err != nil || tag.RowsAffected() != 1 {
				return LifecycleResult{}, transitionError(err)
			}
		}
		return LifecycleResult{SessionID: session.ID, Status: "termination_requested", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: killDeadline}, nil
	})
}

func (store Store) CompleteTermination(ctx context.Context, command TerminatedCommand) (LifecycleResult, error) {
	if !validLifecycleCommandWithoutLease(command.LifecycleCommand) || !runtimeReceiptPattern.MatchString(command.CleanupReceiptHash) || !validUsageManifest(command.UsageManifest) {
		return LifecycleResult{}, ErrInvalidCommand
	}
	return store.withLifecycle(ctx, command.LifecycleCommand, "RuntimeSessionTerminated", "terminated", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, digest []byte, now time.Time, eventID string) (LifecycleResult, error) {
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		if session.Status == "terminated" && session.Version == nextVersion && session.TerminatedAt.Valid && session.TerminatedAt.Time.Equal(occurredAt) && session.CleanupReceiptHash.Valid && session.CleanupReceiptHash.String == command.CleanupReceiptHash && jsonEqual(session.UsageManifest, command.UsageManifest) && allocationTerminalReplay(allocation, nextVersion, eventID) {
			if ok, err := store.lifecycleEventMatches(ctx, tx, session, eventID, nextVersion, "RuntimeSessionTerminated", command.Payload); err != nil || !ok {
				return LifecycleResult{}, replayError(err)
			}
			return LifecycleResult{SessionID: session.ID, Status: "terminated", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Replayed: true}, nil
		}
		if session.Version != command.ExpectedVersion || session.Status != "termination_requested" || !validObservedAt(occurredAt, session.UpdatedAt, now) {
			return LifecycleResult{}, ErrSessionConflict
		}
		if allocation == nil {
			if session.HostID.Valid || command.LeaseToken != "" {
				return LifecycleResult{}, ErrSessionConflict
			}
		} else if allocation.Status != "releasing" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !bytes.Equal(allocation.LeaseHash, digest) || session.ProvisionAttemptID != command.ProvisionAttemptID || session.ProvisionFence != command.ProvisionFence {
			return LifecycleResult{}, ErrSessionConflict
		}
		if err := store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeSessionTerminated", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return LifecycleResult{}, err
		}
		if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='terminated',terminated_at=$2,cleanup_receipt_hash=$3,usage_manifest=$4,last_event_id=$5,updated_at=$2 WHERE tenant_id=$6 AND id=$7 AND version=$8 AND status='termination_requested'`, nextVersion, occurredAt, command.CleanupReceiptHash, command.UsageManifest, eventID, session.TenantID, session.ID, session.Version); err != nil || tag.RowsAffected() != 1 {
			return LifecycleResult{}, transitionError(err)
		}
		if allocation != nil {
			if tag, err := tx.Exec(ctx, `UPDATE agent.runtime_allocations SET version=version+1,status='released',session_version=$1,session_event_id=$2,lease_token_hash=NULL,lease_expires_at=NULL,terminal_reason=$3,terminal_receipt_hash=$4,released_at=$5,updated_at=$5 WHERE tenant_id=$6 AND id=$7 AND version=$8 AND status='releasing'`, nextVersion, eventID, session.TerminationReason.String, command.CleanupReceiptHash, occurredAt, session.TenantID, allocation.ID, allocation.Version); err != nil || tag.RowsAffected() != 1 {
				return LifecycleResult{}, transitionError(err)
			}
		}
		return LifecycleResult{SessionID: session.ID, Status: "terminated", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt}, nil
	})
}

type lifecycleMutation func(pgx.Tx, lifecycleSession, *lifecycleAllocation, []byte, time.Time, string) (LifecycleResult, error)

func (store Store) withLifecycle(ctx context.Context, command LifecycleCommand, eventType, _ string, mutation lifecycleMutation) (LifecycleResult, error) {
	if !store.valid() {
		return LifecycleResult{}, ErrConfiguration
	}
	if err := store.requireEpoch(ctx); err != nil {
		return LifecycleResult{}, err
	}
	var digest []byte
	var err error
	if command.LeaseToken != "" {
		value, digestErr := store.provisionTokens().Digest(command.LeaseToken)
		if digestErr != nil {
			return LifecycleResult{}, ErrInvalidCommand
		}
		digest = value[:]
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LifecycleResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return LifecycleResult{}, err
	}
	session, err := loadLifecycleSession(ctx, tx, command.TenantID, command.SessionID, store.StoreEpoch)
	if err != nil {
		return LifecycleResult{}, err
	}
	allocation, err := loadLifecycleAllocation(ctx, tx, command.TenantID, command.SessionID)
	if err != nil {
		return LifecycleResult{}, err
	}
	eventID, err := store.deterministicID("runtime-lifecycle-event:"+eventType, session.ID, command.ExpectedVersion+1)
	if err != nil {
		return LifecycleResult{}, err
	}
	result, err := mutation(tx, session, allocation, digest, store.now(), eventID)
	if err != nil {
		return LifecycleResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return LifecycleResult{}, err
	}
	return result, nil
}

func loadLifecycleSession(ctx context.Context, tx pgx.Tx, tenantID, sessionID, storeEpoch string) (lifecycleSession, error) {
	var value lifecycleSession
	var idleSeconds, graceSeconds int
	err := tx.QueryRow(ctx, `SELECT s.id::text,s.tenant_id::text,s.user_id::text,s.version,s.status,s.last_event_id::text,
	  COALESCE(s.provision_attempt_id::text,''),s.provision_fence,s.host_id,s.provisioning_at,s.ready_at,s.running_at,
	  s.last_activity_at,s.idle_deadline,s.termination_requested_at,s.kill_deadline,s.termination_reason,s.boot_receipt_hash,
	  s.terminated_at,s.cleanup_receipt_hash,s.usage_manifest,
	  s.execution_deadline,s.updated_at,p.idle_timeout_seconds,p.kill_grace_seconds
	FROM agent.runtime_sessions s JOIN agent.runtime_policy_snapshots p ON p.tenant_id=s.tenant_id AND p.id=s.policy_snapshot_id
	JOIN agent.events le ON le.tenant_id=s.tenant_id AND le.id=s.last_event_id AND le.store_epoch=$3
	WHERE s.tenant_id=$1 AND s.id=$2 FOR UPDATE OF s`, tenantID, sessionID, storeEpoch).Scan(
		&value.ID, &value.TenantID, &value.UserID, &value.Version, &value.Status, &value.LastEventID,
		&value.ProvisionAttemptID, &value.ProvisionFence, &value.HostID, &value.ProvisioningAt, &value.ReadyAt, &value.RunningAt,
		&value.LastActivityAt, &value.IdleDeadline, &value.TerminationRequestedAt, &value.KillDeadline, &value.TerminationReason, &value.BootReceiptHash,
		&value.TerminatedAt, &value.CleanupReceiptHash, &value.UsageManifest,
		&value.ExecutionDeadline, &value.UpdatedAt, &idleSeconds, &graceSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleSession{}, ErrSessionConflict
	}
	value.IdleTimeout, value.KillGrace = time.Duration(idleSeconds)*time.Second, time.Duration(graceSeconds)*time.Second
	return value, err
}

func loadLifecycleAllocation(ctx context.Context, tx pgx.Tx, tenantID, sessionID string) (*lifecycleAllocation, error) {
	var value lifecycleAllocation
	err := tx.QueryRow(ctx, `SELECT id::text,version,status,session_version,session_event_id::text,lease_token_hash,lease_expires_at
	FROM agent.runtime_allocations WHERE tenant_id=$1 AND session_id=$2 FOR UPDATE`, tenantID, sessionID).Scan(
		&value.ID, &value.Version, &value.Status, &value.SessionVersion, &value.SessionEventID, &value.LeaseHash, &value.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &value, err
}

func (store Store) appendLifecycleEvent(ctx context.Context, tx pgx.Tx, session lifecycleSession, eventID string, version uint64, eventType string, occurredAt time.Time, payload PayloadPointer, actor json.RawMessage, correlationID string) error {
	outboxID, err := store.deterministicID("runtime-lifecycle-outbox:"+eventType, session.ID, version)
	if err != nil {
		return err
	}
	publishID, err := store.deterministicID("runtime-lifecycle-publish:"+eventType, session.ID, version)
	if err != nil {
		return err
	}
	causation := session.LastEventID
	_, err = store.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{
		ID: eventID, TenantID: session.TenantID, UserID: session.UserID, EventType: eventType, SchemaVersion: 1,
		AggregateKind: "runtime_session", AggregateID: session.ID, AggregateVersion: version, StoreEpoch: store.StoreEpoch,
		OccurredAt: occurredAt, Actor: actor, CausationID: &causation, CorrelationID: correlationID, PayloadRef: payload.Ref, PayloadHash: payload.Hash,
	}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: payload.Ref, PayloadHash: payload.Hash}}})
	return err
}

func (store Store) lifecycleEventMatches(ctx context.Context, tx pgx.Tx, session lifecycleSession, eventID string, version uint64, eventType string, payload PayloadPointer) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND aggregate_kind='runtime_session' AND aggregate_id=$3 AND aggregate_version=$4 AND event_type=$5 AND payload_ref=$6 AND payload_hash=$7 AND store_epoch=$8)`, session.TenantID, eventID, session.ID, version, eventType, payload.Ref, payload.Hash, store.StoreEpoch).Scan(&exists)
	return exists, err
}

func syncActiveAllocation(ctx context.Context, tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, eventID string, version uint64, occurredAt time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE agent.runtime_allocations SET version=version+1,session_version=$1,session_event_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status='active'`, version, eventID, occurredAt, session.TenantID, allocation.ID, allocation.Version)
	if err != nil || tag.RowsAffected() != 1 {
		return transitionError(err)
	}
	return nil
}

func validAllocationAuthority(session lifecycleSession, allocation *lifecycleAllocation, command LifecycleCommand, digest []byte, at time.Time) bool {
	return validAllocationIdentity(session, allocation, command, digest) && allocation.LeaseExpiresAt.Valid && allocation.LeaseExpiresAt.Time.After(at)
}

func validAllocationIdentity(session lifecycleSession, allocation *lifecycleAllocation, command LifecycleCommand, digest []byte) bool {
	return command.LeaseToken != "" && session.ProvisionAttemptID == command.ProvisionAttemptID && session.ProvisionFence == command.ProvisionFence && bytes.Equal(allocation.LeaseHash, digest)
}

func validLifecycleCommand(command LifecycleCommand) bool {
	return validLifecycleCommandWithoutLease(command) && command.LeaseToken != "" && command.ProvisionAttemptID != "" && command.ProvisionFence > 0
}

func validLifecycleCommandWithoutLease(command LifecycleCommand) bool {
	return command.TenantID != "" && command.SessionID != "" && command.ExpectedVersion > 0 && !command.ObservedAt.IsZero() && validPayload(command.Payload) && validActor(command.Actor) && command.CorrelationID != ""
}

func validObservedAt(value, notBefore, now time.Time) bool {
	return !value.Before(notBefore) && !value.After(now.Add(5*time.Second))
}

func validUsageManifest(value json.RawMessage) bool {
	var manifest struct {
		SchemaVersion       int    `json:"schema_version"`
		VCPUMillis          uint64 `json:"vcpu_millis"`
		WallMillis          uint64 `json:"wall_millis,omitempty"`
		PeakMemoryMiB       uint64 `json:"peak_memory_mib,omitempty"`
		DiskReadBytes       uint64 `json:"disk_read_bytes,omitempty"`
		DiskWriteBytes      uint64 `json:"disk_write_bytes,omitempty"`
		NetworkIngressBytes uint64 `json:"network_ingress_bytes,omitempty"`
		NetworkEgressBytes  uint64 `json:"network_egress_bytes,omitempty"`
		ExitCode            *int   `json:"exit_code,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || manifest.SchemaVersion != 1 {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func terminationSource(status string) bool {
	return status == "requested" || status == "provisioning" || status == "ready" || status == "running" || status == "idle"
}

func terminationAllocationReplay(session lifecycleSession, allocation *lifecycleAllocation, command LifecycleCommand, digest []byte, version uint64, eventID string) bool {
	if allocation == nil {
		return !session.HostID.Valid && command.LeaseToken == ""
	}
	return allocation.Status == "releasing" && allocation.SessionVersion == version && allocation.SessionEventID == eventID && validAllocationIdentity(session, allocation, command, digest)
}

func allocationTerminalReplay(allocation *lifecycleAllocation, version uint64, eventID string) bool {
	return allocation == nil || allocation.Status == "released" && allocation.SessionVersion == version && allocation.SessionEventID == eventID
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func transitionError(err error) error {
	if err != nil {
		return err
	}
	return ErrSessionConflict
}

func replayError(err error) error {
	if err != nil {
		return err
	}
	return ErrSessionConflict
}
