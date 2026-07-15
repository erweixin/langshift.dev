package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	runtimecontract "github.com/langshift/lites/internal/runtime"
)

var runtimeRequestIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{7,127}$`)
var runtimeFailureCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)

type BeginExecutionCommand struct {
	LifecycleCommand
	CapabilityToken string
	RequestID       string
	RequestHash     string
}

type FinishExecutionCommand struct {
	LifecycleCommand
	RequestID       string
	RequestHash     string
	OutcomeHash     string
	OutcomeManifest json.RawMessage
	FailureCode     string
}

// ExecutionOutcomeManifest is the durable, non-secret summary of one guest
// exchange. Stdout/stderr and structured tool output remain encrypted behind
// LifecycleCommand.Payload.
type ExecutionOutcomeManifest struct {
	SchemaVersion       int    `json:"schema_version"`
	RequestID           string `json:"request_id"`
	Kind                string `json:"kind"`
	ExitCode            *int   `json:"exit_code,omitempty"`
	FailureCode         string `json:"failure_code,omitempty"`
	TimedOut            bool   `json:"timed_out"`
	OutputTruncated     bool   `json:"output_truncated"`
	StdoutBytes         int64  `json:"stdout_bytes"`
	StderrBytes         int64  `json:"stderr_bytes"`
	UserCPUTimeMillis   int64  `json:"user_cpu_time_millis"`
	SystemCPUTimeMillis int64  `json:"system_cpu_time_millis"`
	StartedUnixMillis   int64  `json:"started_unix_millis,omitempty"`
	FinishedUnixMillis  int64  `json:"finished_unix_millis,omitempty"`
	ObservedUnixMillis  int64  `json:"observed_unix_millis,omitempty"`
	TerminationReason   string `json:"termination_reason,omitempty"`
}

type RuntimeExecution struct {
	ID, TenantID, UserID, SessionID, ToolCallID string
	RequestID, RequestHash, Status              string
	Version, StartedSessionVersion              uint64
	StartedEventID                              string
	StartedAt                                   time.Time
	FinishedSessionVersion                      uint64
	FinishedEventID                             string
	FinishedAt                                  time.Time
	Outcome                                     PayloadPointer
	OutcomeHash                                 string
	OutcomeManifest                             json.RawMessage
	FailureCode                                 string
	Lifecycle                                   LifecycleResult
	Replayed                                    bool
}

func (store Store) BeginExecution(ctx context.Context, command BeginExecutionCommand) (RuntimeExecution, error) {
	if !store.valid() {
		return RuntimeExecution{}, ErrConfiguration
	}
	if !validLifecycleCommand(command.LifecycleCommand) || command.CapabilityToken == "" || !runtimeRequestIDPattern.MatchString(command.RequestID) || !runtimeReceiptPattern.MatchString(command.RequestHash) {
		return RuntimeExecution{}, ErrInvalidCommand
	}
	claims, err := store.Verifier.Verify(command.CapabilityToken, store.now())
	if err != nil {
		return RuntimeExecution{}, err
	}
	if claims.TenantID != command.TenantID {
		return RuntimeExecution{}, ErrCapabilityBinding
	}
	executionID, err := store.deterministicID("runtime-execution:"+command.RequestID, command.SessionID, 1)
	if err != nil {
		return RuntimeExecution{}, ErrConfiguration
	}
	return store.withExecutionLifecycle(ctx, command.LifecycleCommand, "RuntimeSessionExecutionStarted", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, digest []byte, now time.Time, eventID string) (RuntimeExecution, error) {
		if err := authorizeExecutionCapability(ctx, tx, store, claims, session, now); err != nil {
			return RuntimeExecution{}, err
		}
		existing, loadErr := loadRuntimeExecution(ctx, tx, command.TenantID, command.SessionID, command.RequestID)
		if loadErr == nil {
			if existing.ID != executionID || existing.RequestHash != command.RequestHash {
				return RuntimeExecution{}, ErrSessionConflict
			}
			if existing.Status == "running" && (session.Status != "running" || session.Version != existing.StartedSessionVersion || session.LastEventID != existing.StartedEventID) {
				return RuntimeExecution{}, ErrSessionConflict
			}
			existing.Replayed = true
			return existing, nil
		}
		if !errors.Is(loadErr, pgx.ErrNoRows) {
			return RuntimeExecution{}, loadErr
		}
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		if session.Version != command.ExpectedVersion || session.Status != "ready" && session.Status != "idle" || allocation == nil || allocation.Status != "active" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !validAllocationAuthority(session, allocation, command.LifecycleCommand, digest, occurredAt) {
			return RuntimeExecution{}, ErrSessionConflict
		}
		if !validObservedAt(occurredAt, session.UpdatedAt, now) || !session.ExecutionDeadline.After(occurredAt) {
			return RuntimeExecution{}, ErrInvalidCommand
		}
		var executionRight string
		if err := tx.QueryRow(ctx, `SELECT agent.runtime_lock_execution_right($1,$2,NULLIF($3,'')::uuid,NULLIF($4,0),$5)`, session.TenantID, session.ID, claims.ApprovalID, claims.ApprovalVersion, now).Scan(&executionRight); err != nil {
			return RuntimeExecution{}, err
		}
		switch executionRight {
		case "approval_invalid":
			return RuntimeExecution{}, ErrApproval
		case "execution_invalid":
			return RuntimeExecution{}, ErrCapabilityBinding
		case "valid":
		default:
			return RuntimeExecution{}, ErrConfiguration
		}
		if err := store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeSessionExecutionStarted", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return RuntimeExecution{}, err
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='running',running_at=COALESCE(running_at,$2),last_activity_at=$2,idle_deadline=NULL,last_event_id=$3,updated_at=$2 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status=$7`, nextVersion, occurredAt, eventID, session.TenantID, session.ID, session.Version, session.Status); updateErr != nil || tag.RowsAffected() != 1 {
			return RuntimeExecution{}, transitionError(updateErr)
		}
		if err := syncActiveAllocation(ctx, tx, session, allocation, eventID, nextVersion, occurredAt); err != nil {
			return RuntimeExecution{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent.runtime_executions(id,tenant_id,user_id,session_id,tool_call_id,version,request_id,request_hash,status,started_session_version,started_event_id,started_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,1,$6,$7,'running',$8,$9,$10,$10,$10)`, executionID, session.TenantID, session.UserID, session.ID, session.ToolCallID, command.RequestID, command.RequestHash, nextVersion, eventID, occurredAt); err != nil {
			return RuntimeExecution{}, err
		}
		lifecycle := LifecycleResult{SessionID: session.ID, Status: "running", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: session.ExecutionDeadline}
		return RuntimeExecution{ID: executionID, TenantID: session.TenantID, UserID: session.UserID, SessionID: session.ID, ToolCallID: session.ToolCallID, RequestID: command.RequestID, RequestHash: command.RequestHash, Status: "running", Version: 1, StartedSessionVersion: nextVersion, StartedEventID: eventID, StartedAt: occurredAt, Lifecycle: lifecycle}, nil
	})
}

func authorizeExecutionCapability(ctx context.Context, tx pgx.Tx, store Store, claims runtimecontract.CapabilityClaims, lifecycle lifecycleSession, now time.Time) error {
	session, policy, err := loadSessionForProvision(ctx, tx, claims, store.StoreEpoch)
	if err != nil {
		return err
	}
	if session.ID != lifecycle.ID || session.TenantID != lifecycle.TenantID || session.UserID != lifecycle.UserID || session.ToolCallID != lifecycle.ToolCallID || session.Version != lifecycle.Version || session.Status != lifecycle.Status {
		return ErrCapabilityBinding
	}
	return validateCapabilityBinding(claims, session, policy, now)
}

func (store Store) CompleteExecution(ctx context.Context, command FinishExecutionCommand) (RuntimeExecution, error) {
	if !validFinishExecution(command, "completed") {
		return RuntimeExecution{}, ErrInvalidCommand
	}
	return store.withExecutionLifecycle(ctx, command.LifecycleCommand, "RuntimeSessionIdle", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, digest []byte, now time.Time, eventID string) (RuntimeExecution, error) {
		execution, err := loadRuntimeExecution(ctx, tx, command.TenantID, command.SessionID, command.RequestID)
		if err != nil || execution.RequestHash != command.RequestHash {
			return RuntimeExecution{}, ErrSessionConflict
		}
		if execution.Status == "completed" {
			if !exactExecutionOutcome(execution, command, "completed") {
				return RuntimeExecution{}, ErrSessionConflict
			}
			execution.Replayed = true
			return execution, nil
		}
		if execution.Status != "running" {
			return RuntimeExecution{}, ErrSessionConflict
		}
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		idleDeadline := occurredAt.Add(session.IdleTimeout)
		if idleDeadline.After(session.ExecutionDeadline) {
			idleDeadline = session.ExecutionDeadline
		}
		if session.Version != command.ExpectedVersion || command.ExpectedVersion != execution.StartedSessionVersion || session.Status != "running" || session.LastEventID != execution.StartedEventID || allocation == nil || allocation.Status != "active" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !validAllocationAuthority(session, allocation, command.LifecycleCommand, digest, occurredAt) {
			return RuntimeExecution{}, ErrSessionConflict
		}
		if !validObservedAt(occurredAt, session.UpdatedAt, now) || !idleDeadline.After(occurredAt) {
			return RuntimeExecution{}, ErrInvalidCommand
		}
		if err = store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeSessionIdle", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return RuntimeExecution{}, err
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='idle',last_activity_at=$2,idle_deadline=$3,last_event_id=$4,updated_at=$2 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status='running'`, nextVersion, occurredAt, idleDeadline, eventID, session.TenantID, session.ID, session.Version); updateErr != nil || tag.RowsAffected() != 1 {
			return RuntimeExecution{}, transitionError(updateErr)
		}
		if err = syncActiveAllocation(ctx, tx, session, allocation, eventID, nextVersion, occurredAt); err != nil {
			return RuntimeExecution{}, err
		}
		if err = finishRuntimeExecution(ctx, tx, execution, command, "completed", eventID, nextVersion, occurredAt); err != nil {
			return RuntimeExecution{}, err
		}
		return finishedExecution(execution, command, "completed", eventID, nextVersion, occurredAt, LifecycleResult{SessionID: session.ID, Status: "idle", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: idleDeadline}), nil
	})
}

func (store Store) MarkExecutionOutcomeUnknown(ctx context.Context, command FinishExecutionCommand) (RuntimeExecution, error) {
	if !validFinishExecution(command, "outcome_unknown") {
		return RuntimeExecution{}, ErrInvalidCommand
	}
	return store.withExecutionLifecycle(ctx, command.LifecycleCommand, "RuntimeTerminationRequested", func(tx pgx.Tx, session lifecycleSession, allocation *lifecycleAllocation, digest []byte, now time.Time, eventID string) (RuntimeExecution, error) {
		execution, err := loadRuntimeExecution(ctx, tx, command.TenantID, command.SessionID, command.RequestID)
		if err != nil || execution.RequestHash != command.RequestHash {
			return RuntimeExecution{}, ErrSessionConflict
		}
		if execution.Status == "outcome_unknown" {
			if !exactExecutionOutcome(execution, command, "outcome_unknown") {
				return RuntimeExecution{}, ErrSessionConflict
			}
			execution.Replayed = true
			return execution, nil
		}
		if execution.Status != "running" {
			return RuntimeExecution{}, ErrSessionConflict
		}
		occurredAt, nextVersion := command.ObservedAt.UTC().Truncate(time.Microsecond), command.ExpectedVersion+1
		killDeadline := occurredAt.Add(session.KillGrace)
		if session.Version != command.ExpectedVersion || command.ExpectedVersion != execution.StartedSessionVersion || session.Status != "running" || session.LastEventID != execution.StartedEventID || allocation == nil || allocation.Status != "active" || allocation.SessionVersion != session.Version || allocation.SessionEventID != session.LastEventID || !validAllocationIdentity(session, allocation, command.LifecycleCommand, digest) || !validObservedAt(occurredAt, session.UpdatedAt, now) {
			return RuntimeExecution{}, ErrSessionConflict
		}
		if err = store.appendLifecycleEvent(ctx, tx, session, eventID, nextVersion, "RuntimeTerminationRequested", occurredAt, command.Payload, command.Actor, command.CorrelationID); err != nil {
			return RuntimeExecution{}, err
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=$1,status='termination_requested',provision_lease_hash=NULL,provision_lease_expires_at=NULL,termination_requested_at=$2,kill_deadline=$3,termination_reason='execution_outcome_unknown',last_event_id=$4,updated_at=$2 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status='running'`, nextVersion, occurredAt, killDeadline, eventID, session.TenantID, session.ID, session.Version); updateErr != nil || tag.RowsAffected() != 1 {
			return RuntimeExecution{}, transitionError(updateErr)
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runtime_allocations SET version=version+1,status='releasing',session_version=$1,session_event_id=$2,lease_expires_at=$3,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status='active'`, nextVersion, eventID, killDeadline, occurredAt, session.TenantID, allocation.ID, allocation.Version); updateErr != nil || tag.RowsAffected() != 1 {
			return RuntimeExecution{}, transitionError(updateErr)
		}
		if err = finishRuntimeExecution(ctx, tx, execution, command, "outcome_unknown", eventID, nextVersion, occurredAt); err != nil {
			return RuntimeExecution{}, err
		}
		return finishedExecution(execution, command, "outcome_unknown", eventID, nextVersion, occurredAt, LifecycleResult{SessionID: session.ID, Status: "termination_requested", EventID: eventID, Version: nextVersion, OccurredAt: occurredAt, Deadline: killDeadline}), nil
	})
}

type executionMutation func(pgx.Tx, lifecycleSession, *lifecycleAllocation, []byte, time.Time, string) (RuntimeExecution, error)

func (store Store) withExecutionLifecycle(ctx context.Context, command LifecycleCommand, eventType string, mutation executionMutation) (RuntimeExecution, error) {
	if !store.valid() {
		return RuntimeExecution{}, ErrConfiguration
	}
	if err := store.requireEpoch(ctx); err != nil {
		return RuntimeExecution{}, err
	}
	digestValue, err := store.provisionTokens().Digest(command.LeaseToken)
	if err != nil {
		return RuntimeExecution{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RuntimeExecution{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return RuntimeExecution{}, err
	}
	session, err := loadLifecycleSession(ctx, tx, command.TenantID, command.SessionID, store.StoreEpoch)
	if err != nil {
		return RuntimeExecution{}, err
	}
	allocation, err := loadLifecycleAllocation(ctx, tx, command.TenantID, command.SessionID)
	if err != nil {
		return RuntimeExecution{}, err
	}
	eventID, err := store.deterministicID("runtime-lifecycle-event:"+eventType, session.ID, command.ExpectedVersion+1)
	if err != nil {
		return RuntimeExecution{}, err
	}
	result, err := mutation(tx, session, allocation, digestValue[:], store.now(), eventID)
	if err != nil {
		return RuntimeExecution{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RuntimeExecution{}, err
	}
	return result, nil
}

func loadRuntimeExecution(ctx context.Context, tx pgx.Tx, tenantID, sessionID, requestID string) (RuntimeExecution, error) {
	var value RuntimeExecution
	var finishedVersion sql.NullInt64
	var finishedEvent, outcomeRef, payloadHash, outcomeHash, failure sql.NullString
	var finishedAt sql.NullTime
	err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,user_id::text,session_id::text,tool_call_id::text,version,request_id,request_hash,status,started_session_version,started_event_id::text,started_at,finished_session_version,finished_event_id::text,finished_at,outcome_ref,outcome_payload_hash,outcome_hash,outcome_manifest,failure_code FROM agent.runtime_executions WHERE tenant_id=$1 AND session_id=$2 AND request_id=$3 FOR UPDATE`, tenantID, sessionID, requestID).Scan(&value.ID, &value.TenantID, &value.UserID, &value.SessionID, &value.ToolCallID, &value.Version, &value.RequestID, &value.RequestHash, &value.Status, &value.StartedSessionVersion, &value.StartedEventID, &value.StartedAt, &finishedVersion, &finishedEvent, &finishedAt, &outcomeRef, &payloadHash, &outcomeHash, &value.OutcomeManifest, &failure)
	if err != nil {
		return RuntimeExecution{}, err
	}
	if finishedVersion.Valid && finishedVersion.Int64 > 0 {
		value.FinishedSessionVersion = uint64(finishedVersion.Int64)
	}
	if finishedEvent.Valid {
		value.FinishedEventID = finishedEvent.String
	}
	if finishedAt.Valid {
		value.FinishedAt = finishedAt.Time
	}
	if outcomeRef.Valid {
		value.Outcome.Ref = outcomeRef.String
	}
	if payloadHash.Valid {
		value.Outcome.Hash = payloadHash.String
	}
	if outcomeHash.Valid {
		value.OutcomeHash = outcomeHash.String
	}
	if failure.Valid {
		value.FailureCode = failure.String
	}
	return value, nil
}

func finishRuntimeExecution(ctx context.Context, tx pgx.Tx, execution RuntimeExecution, command FinishExecutionCommand, status, eventID string, sessionVersion uint64, occurredAt time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE agent.runtime_executions SET version=2,status=$1,finished_session_version=$2,finished_event_id=$3,finished_at=$4,outcome_ref=$5,outcome_payload_hash=$6,outcome_hash=$7,outcome_manifest=$8,failure_code=NULLIF($9,''),updated_at=$4 WHERE tenant_id=$10 AND id=$11 AND version=1 AND status='running'`, status, sessionVersion, eventID, occurredAt, command.Payload.Ref, command.Payload.Hash, command.OutcomeHash, command.OutcomeManifest, command.FailureCode, execution.TenantID, execution.ID)
	if err != nil || tag.RowsAffected() != 1 {
		return transitionError(err)
	}
	return nil
}

// settleRunningExecutionUnknown closes the idempotency record in the same
// transaction as an independently-authorized session termination. A caller
// must never leave an execution eligible for retry after the guest may have
// observed the request.
func settleRunningExecutionUnknown(ctx context.Context, tx pgx.Tx, session lifecycleSession, eventID string, sessionVersion uint64, occurredAt time.Time, evidence PayloadPointer, terminationReason string) error {
	if session.Status != "running" {
		return nil
	}
	var requestID string
	err := tx.QueryRow(ctx, `SELECT request_id FROM agent.runtime_executions WHERE tenant_id=$1 AND session_id=$2 AND status='running' FOR UPDATE`, session.TenantID, session.ID).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !runtimeReceiptPattern.MatchString(evidence.Hash) || terminationReason == "" || len(terminationReason) > 128 {
		return ErrInvalidCommand
	}
	execution, err := loadRuntimeExecution(ctx, tx, session.TenantID, session.ID, requestID)
	if err != nil || execution.Status != "running" || execution.StartedSessionVersion+1 != sessionVersion {
		return errors.Join(err, ErrSessionConflict)
	}
	const failureCode = "execution_interrupted"
	manifest, err := json.Marshal(ExecutionOutcomeManifest{
		SchemaVersion:      1,
		RequestID:          execution.RequestID,
		Kind:               "transport_unknown",
		FailureCode:        failureCode,
		ObservedUnixMillis: occurredAt.UnixMilli(),
		TerminationReason:  terminationReason,
	})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(manifest)
	return finishRuntimeExecution(ctx, tx, execution, FinishExecutionCommand{
		RequestID:       execution.RequestID,
		RequestHash:     execution.RequestHash,
		OutcomeHash:     hex.EncodeToString(digest[:]),
		OutcomeManifest: manifest,
		FailureCode:     failureCode,
		LifecycleCommand: LifecycleCommand{
			Payload: evidence,
		},
	}, "outcome_unknown", eventID, sessionVersion, occurredAt)
}

func finishedExecution(execution RuntimeExecution, command FinishExecutionCommand, status, eventID string, sessionVersion uint64, occurredAt time.Time, lifecycle LifecycleResult) RuntimeExecution {
	execution.Version, execution.Status = 2, status
	execution.FinishedSessionVersion, execution.FinishedEventID, execution.FinishedAt = sessionVersion, eventID, occurredAt
	execution.Outcome, execution.OutcomeHash = command.Payload, command.OutcomeHash
	execution.OutcomeManifest, execution.FailureCode, execution.Lifecycle = append(json.RawMessage(nil), command.OutcomeManifest...), command.FailureCode, lifecycle
	return execution
}

func exactExecutionOutcome(execution RuntimeExecution, command FinishExecutionCommand, status string) bool {
	return execution.Status == status && execution.RequestHash == command.RequestHash && execution.Outcome == command.Payload && execution.OutcomeHash == command.OutcomeHash && execution.FailureCode == command.FailureCode && jsonEqual(execution.OutcomeManifest, command.OutcomeManifest)
}

func validFinishExecution(command FinishExecutionCommand, status string) bool {
	if !validLifecycleCommand(command.LifecycleCommand) || !runtimeRequestIDPattern.MatchString(command.RequestID) || !runtimeReceiptPattern.MatchString(command.RequestHash) || !runtimeReceiptPattern.MatchString(command.OutcomeHash) || !validOutcomeManifest(command.OutcomeManifest, command.RequestID, status, command.FailureCode) {
		return false
	}
	return (status == "completed" && command.FailureCode == "") || (status == "outcome_unknown" && runtimeFailureCodePattern.MatchString(command.FailureCode))
}

func validOutcomeManifest(value json.RawMessage, requestID, status, failureCode string) bool {
	var manifest ExecutionOutcomeManifest
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || manifest.SchemaVersion != 1 || manifest.RequestID != requestID || manifest.StdoutBytes < 0 || manifest.StderrBytes < 0 || manifest.UserCPUTimeMillis < 0 || manifest.SystemCPUTimeMillis < 0 {
		return false
	}
	if status == "outcome_unknown" {
		return manifest.Kind == "transport_unknown" && manifest.ExitCode == nil && manifest.FailureCode == failureCode && manifest.ObservedUnixMillis > 0 && manifest.StartedUnixMillis == 0 && manifest.FinishedUnixMillis == 0 && !manifest.TimedOut && !manifest.OutputTruncated && len(manifest.TerminationReason) <= 128
	}
	if manifest.Kind != "guest_result" || manifest.ExitCode == nil || manifest.ObservedUnixMillis != 0 || manifest.TerminationReason != "" || manifest.StartedUnixMillis <= 0 || manifest.FinishedUnixMillis < manifest.StartedUnixMillis {
		return false
	}
	knownFailure := manifest.FailureCode == "" || manifest.FailureCode == "process_failed" || manifest.FailureCode == "deadline_exceeded" || manifest.FailureCode == "cancelled" || manifest.FailureCode == "output_limit_exceeded" || manifest.FailureCode == "transport_failed"
	consistent := manifest.FailureCode != "" || *manifest.ExitCode == 0 && !manifest.TimedOut && !manifest.OutputTruncated
	return knownFailure && consistent && (!manifest.TimedOut || manifest.FailureCode == "deadline_exceeded") && (!manifest.OutputTruncated || manifest.FailureCode == "output_limit_exceeded" || manifest.FailureCode == "transport_failed")
}
