package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

var ErrExecutionRightConflict = errors.New("agent run execution right is stale, expired, or inconsistent")

// HeartbeatRun extends one exact execution right across all three durable
// owners. A partial renewal is forbidden because it could make the inbox,
// aggregate, and attempt disagree about whether the Worker may still commit.
func (store RunStore) HeartbeatRun(ctx context.Context, claim RunClaim) (RunClaim, error) {
	if !store.validClaim() || !validRunClaim(claim) {
		return RunClaim{}, ErrConfiguration
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return RunClaim{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return RunClaim{}, ErrExecutionRightConflict
	}
	now := store.claimNow()
	expiresAt := now.Add(store.LeaseTTL).UTC().Truncate(time.Microsecond)
	if !expiresAt.After(claim.LeaseExpiresAt) {
		return RunClaim{}, ErrExecutionRightConflict
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RunClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return RunClaim{}, err
	}
	if err = lockRunInbox(ctx, tx, claim, digest[:], now); err != nil {
		return RunClaim{}, err
	}
	var locked string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND status='executing' AND run_version=$4 AND active_command_id=$5 AND active_attempt_id=$6 AND current_fence=$7 AND lease_token_hash=$8 AND lease_expires_at=$9 AND lease_expires_at>$10 FOR UPDATE`, claim.RunID, claim.TenantID, claim.UserID, claim.RunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&locked)
	if err != nil || locked != claim.RunID {
		return RunClaim{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return RunClaim{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET lease_expires_at=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND status='running' AND owner_attempt_id=$5 AND fence=$6 AND lease_token_hash=$7 AND lease_expires_at=$8`, expiresAt, now, claim.InboxID, claim.TenantID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return RunClaim{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET lease_expires_at=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND status='executing' AND run_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10`, expiresAt, now, claim.RunID, claim.TenantID, claim.RunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return RunClaim{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,lease_expires_at=$2,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND status='running' AND version=$6 AND command_id=$7 AND fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10`, attemptVersion+1, expiresAt, now, claim.AttemptID, claim.TenantID, attemptVersion, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return RunClaim{}, ErrExecutionRightConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return RunClaim{}, err
	}
	claim.LeaseExpiresAt = expiresAt
	return claim, nil
}

type CompleteRunCommand struct {
	Claim                 RunClaim
	ExpectedRunVersion    uint64
	TargetState           statemachine.RunState
	ResultHash            string
	Actor                 json.RawMessage
	CorrelationID         string
	RunEvent              PayloadPointer
	AttemptCompletedEvent PayloadPointer
}

type CompletedRun struct {
	RunID          string
	RunVersion     uint64
	Status         statemachine.RunState
	AttemptStatus  statemachine.AttemptState
	CompletedAt    time.Time
	RunEventID     string
	AttemptEventID string
}

// CompleteRunTerminal is the commit point for a terminal AgentWorker result.
// The business event, inbox completion, attempt/job terminal state, and active
// execution-right release are indivisible.
func (store RunStore) CompleteRunTerminal(ctx context.Context, command CompleteRunCommand) (CompletedRun, error) {
	claim := command.Claim
	if !store.validClaim() || !validRunClaim(claim) || !validCompleteRun(command) {
		return CompletedRun{}, ErrConfiguration
	}
	if err := statemachine.Runs.ValidateTransition(statemachine.RunExecuting, command.TargetState); err != nil || !statemachine.Runs.IsTerminal(command.TargetState) {
		return CompletedRun{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return CompletedRun{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return CompletedRun{}, ErrExecutionRightConflict
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CompletedRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return CompletedRun{}, err
	}
	if err = lockRunInbox(ctx, tx, claim, digest[:], now); err != nil {
		return CompletedRun{}, err
	}
	var userID string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND status='executing' AND run_version=$4 AND active_command_id=$5 AND active_attempt_id=$6 AND current_fence=$7 AND lease_token_hash=$8 AND lease_expires_at=$9 AND lease_expires_at>$10 FOR UPDATE`, claim.RunID, claim.TenantID, claim.UserID, command.ExpectedRunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&userID)
	if err != nil {
		return CompletedRun{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return CompletedRun{}, ErrExecutionRightConflict
	}
	attemptState, jobState := terminalExecutionStates(command.TargetState)
	nextRunVersion := command.ExpectedRunVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status=$1,run_version=$2,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$3 WHERE id=$4 AND tenant_id=$5 AND status='executing' AND run_version=$6 AND active_command_id=$7 AND active_attempt_id=$8 AND current_fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$3`, command.TargetState, nextRunVersion, now, claim.RunID, claim.TenantID, command.ExpectedRunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedRun{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedRun{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status=$2,finished_at=$3,result_hash=$4,updated_at=$3 WHERE id=$5 AND tenant_id=$6 AND version=$7 AND job_id=$8 AND command_id=$9 AND status='running' AND fence=$10 AND lease_token_hash=$11 AND lease_expires_at=$12 AND lease_expires_at>$3`, attemptVersion+1, attemptState, now, command.ResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedRun{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status=$1,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND command_id=$5 AND status='running'`, jobState, now, claim.JobID, claim.TenantID, claim.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedRun{}, ErrExecutionRightConflict
	}
	eventIDs, err := store.completionEventIdentifiers(claim.AttemptID, command.TargetState)
	if err != nil {
		return CompletedRun{}, err
	}
	causationID := claim.CommandID
	runEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.runEvent, TenantID: claim.TenantID, UserID: userID, EventType: terminalRunEventType(command.TargetState), SchemaVersion: 1, AggregateKind: "run", AggregateID: claim.RunID, AggregateVersion: nextRunVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.RunEvent.Ref, PayloadHash: command.RunEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.runOutbox, CommandID: eventIDs.runPublish, CommandType: "events.publish", PayloadRef: command.RunEvent.Ref, PayloadHash: command.RunEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, runEvent); err != nil {
		return CompletedRun{}, err
	}
	attemptCausationID := eventIDs.runEvent
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.attemptEvent, TenantID: claim.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &attemptCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.attemptOutbox, CommandID: eventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return CompletedRun{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CompletedRun{}, err
	}
	return CompletedRun{RunID: claim.RunID, RunVersion: nextRunVersion, Status: command.TargetState, AttemptStatus: attemptState, CompletedAt: now, RunEventID: eventIDs.runEvent, AttemptEventID: eventIDs.attemptEvent}, nil
}

func lockRunInbox(ctx context.Context, tx pgx.Tx, claim RunClaim, digest []byte, now time.Time) error {
	var locked string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent.inbox WHERE id=$1 AND tenant_id=$2 AND store_epoch=$3 AND consumer_name=$4 AND command_id=$5 AND request_hash=$6 AND status='running' AND owner_attempt_id=$7 AND fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$11 FOR UPDATE`, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest, claim.LeaseExpiresAt, now).Scan(&locked)
	if err != nil || locked != claim.InboxID {
		return ErrExecutionRightConflict
	}
	return nil
}

type completionEventIDs struct {
	runEvent, runOutbox, runPublish, attemptEvent, attemptOutbox, attemptPublish string
}

func (store RunStore) completionEventIdentifiers(attemptID string, state statemachine.RunState) (completionEventIDs, error) {
	domains := []string{"run-completed-event", "run-completed-publish-outbox", "run-completed-publish-command", "job-attempt-completed-event", "job-attempt-completed-publish-outbox", "job-attempt-completed-publish-command"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, attemptID+"\x00"+string(state))
		if err != nil {
			return completionEventIDs{}, err
		}
		values[index] = value
	}
	return completionEventIDs{values[0], values[1], values[2], values[3], values[4], values[5]}, nil
}

func validRunClaim(claim RunClaim) bool {
	return claim.RunID != "" && claim.TenantID != "" && claim.UserID != "" && claim.StoreEpoch != "" && claim.RunVersion > 0 && claim.CommandID != "" && claim.ConsumerName != "" && claim.RequestHash != "" && claim.JobID != "" && claim.InboxID != "" && claim.AttemptID != "" && claim.Fence > 0 && claim.LeaseToken != "" && !claim.LeaseExpiresAt.IsZero() && !claim.Completed
}

func validCompleteRun(command CompleteRunCommand) bool {
	return command.ExpectedRunVersion == command.Claim.RunVersion && command.ResultHash != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.RunEvent) && validPointer(command.AttemptCompletedEvent)
}

func (store RunStore) requireClaimEpoch(ctx context.Context, expected string) error {
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrConfiguration
	}
	if current != expected || current != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store RunStore) claimNow() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func terminalExecutionStates(state statemachine.RunState) (statemachine.AttemptState, string) {
	switch state {
	case statemachine.RunSucceeded:
		return statemachine.AttemptSucceeded, "succeeded"
	case statemachine.RunFailed:
		return statemachine.AttemptFailed, "failed"
	case statemachine.RunCancelled:
		return statemachine.AttemptAbandoned, "cancelled"
	default:
		return statemachine.AttemptExpired, "expired"
	}
}

func terminalRunEventType(state statemachine.RunState) string {
	switch state {
	case statemachine.RunSucceeded:
		return "RunSucceeded"
	case statemachine.RunFailed:
		return "RunFailed"
	case statemachine.RunCancelled:
		return "RunCancelled"
	default:
		return "RunExpired"
	}
}
