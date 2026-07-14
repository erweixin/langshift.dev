package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

var (
	ErrClaimBusy       = errors.New("run command already has a live execution right")
	ErrClaimCompleted  = errors.New("run command was already completed")
	ErrClaimConflict   = errors.New("run command claim conflicts with durable state")
	ErrRunNotClaimable = errors.New("run is not claimable by this command")
	ErrStaleEpoch      = errors.New("run command belongs to a stale store epoch")
)

type ClaimRunCommand struct {
	Command             eventpostgres.DeliveredCommand
	ConsumerName        string
	WorkerID            string
	Actor               json.RawMessage
	CorrelationID       string
	RunEvent            PayloadPointer
	AttemptStartedEvent PayloadPointer
	AttemptExpiredEvent PayloadPointer
}

type RunClaim struct {
	RunID          string
	TenantID       string
	UserID         string
	StoreEpoch     string
	RunVersion     uint64
	CommandID      string
	ConsumerName   string
	RequestHash    string
	JobID          string
	InboxID        string
	AttemptID      string
	Fence          uint64
	LeaseToken     string
	LeaseExpiresAt time.Time
	Completed      bool
}

// ClaimStart installs the only valid execution right for a queued Run. Inbox
// dedupe is locked before the Run aggregate, and the inbox, Run projection,
// Job/Attempt projections, events, and outbox publications commit atomically.
func (store RunStore) ClaimStart(ctx context.Context, command ClaimRunCommand) (RunClaim, error) {
	if !store.validClaim() {
		return RunClaim{}, ErrConfiguration
	}
	if !validClaimRun(command) {
		return RunClaim{}, ErrInvalidCommand
	}
	currentEpoch, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || currentEpoch == "" {
		return RunClaim{}, ErrConfiguration
	}
	if currentEpoch != command.Command.StoreEpoch || currentEpoch != store.StoreEpoch {
		return RunClaim{}, ErrStaleEpoch
	}
	random := store.Random
	if random == nil {
		random = rand.Reader
	}
	inboxID, err := ids.NewUUIDFrom(random)
	if err != nil {
		return RunClaim{}, err
	}
	attemptID, err := ids.NewUUIDFrom(random)
	if err != nil {
		return RunClaim{}, err
	}
	manager := store.Tokens
	manager.Random = random
	credential, err := manager.Issue()
	if err != nil {
		return RunClaim{}, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if store.Now != nil {
		now = store.Now().UTC().Truncate(time.Microsecond)
	}
	expiresAt := now.Add(store.LeaseTTL).UTC().Truncate(time.Microsecond)

	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RunClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.Command.TenantID); err != nil {
		return RunClaim{}, err
	}
	var currentFence uint64
	if err = tx.QueryRow(ctx, `SELECT current_fence FROM agent.runs WHERE id=$1 AND tenant_id=$2`, command.Command.AggregateID, command.Command.TenantID).Scan(&currentFence); errors.Is(err, pgx.ErrNoRows) {
		return RunClaim{}, ErrRunNotClaimable
	} else if err != nil {
		return RunClaim{}, err
	}
	candidateFence := currentFence + 1
	tag, err := tx.Exec(ctx, `INSERT INTO agent.inbox (id,tenant_id,store_epoch,consumer_name,command_id,status,owner_attempt_id,fence,lease_token_hash,lease_expires_at,request_hash) VALUES ($1,$2,$3,$4,$5,'running',$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, inboxID, command.Command.TenantID, command.Command.StoreEpoch, command.ConsumerName, command.Command.CommandID, attemptID, candidateFence, credential.Digest[:], expiresAt, command.Command.PayloadHash)
	if err != nil {
		return RunClaim{}, err
	}
	if tag.RowsAffected() == 0 {
		var actualInboxID, actualEpoch, status, actualAttempt, requestHash string
		var actualFence uint64
		var actualExpiry time.Time
		var actualDigest []byte
		err = tx.QueryRow(ctx, `SELECT id::text,store_epoch::text,status,owner_attempt_id::text,fence,lease_token_hash,lease_expires_at,request_hash FROM agent.inbox WHERE tenant_id=$1 AND consumer_name=$2 AND command_id=$3 FOR UPDATE`, command.Command.TenantID, command.ConsumerName, command.Command.CommandID).Scan(&actualInboxID, &actualEpoch, &status, &actualAttempt, &actualFence, &actualDigest, &actualExpiry, &requestHash)
		if err != nil {
			return RunClaim{}, err
		}
		if actualEpoch != command.Command.StoreEpoch || requestHash != command.Command.PayloadHash {
			return RunClaim{}, ErrClaimConflict
		}
		if status == "completed" {
			return RunClaim{RunID: command.Command.AggregateID, TenantID: command.Command.TenantID, StoreEpoch: command.Command.StoreEpoch, CommandID: command.Command.CommandID, ConsumerName: command.ConsumerName, RequestHash: command.Command.PayloadHash, InboxID: actualInboxID, AttemptID: actualAttempt, Fence: actualFence, LeaseExpiresAt: actualExpiry, Completed: true}, ErrClaimCompleted
		}
		if status == "running" && actualExpiry.After(now) {
			return RunClaim{RunID: command.Command.AggregateID, TenantID: command.Command.TenantID, StoreEpoch: command.Command.StoreEpoch, CommandID: command.Command.CommandID, ConsumerName: command.ConsumerName, RequestHash: command.Command.PayloadHash, InboxID: actualInboxID, AttemptID: actualAttempt, Fence: actualFence, LeaseExpiresAt: actualExpiry}, ErrClaimBusy
		}
		if status == "running" && !actualExpiry.After(now) {
			result, reclaimErr := store.reclaimExpiredRun(ctx, tx, command, expiredRunClaim{
				inboxID: actualInboxID, oldAttemptID: actualAttempt, oldFence: actualFence,
				oldDigest: actualDigest, oldExpiry: actualExpiry, newAttemptID: attemptID,
				newFence: candidateFence, newDigest: credential.Digest[:], newToken: credential.Raw,
				newExpiry: expiresAt, now: now,
			})
			if reclaimErr != nil {
				return RunClaim{}, reclaimErr
			}
			if err = tx.Commit(ctx); err != nil {
				return RunClaim{}, err
			}
			return result, nil
		}
		return RunClaim{}, ErrRunNotClaimable
	}

	var userID, status, pendingCommand string
	var runVersion, lockedFence uint64
	var cancelRequested *time.Time
	var dueAt time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,status,run_version,pending_command_id::text,current_fence,cancel_requested_at,due_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, command.Command.AggregateID, command.Command.TenantID).Scan(&userID, &status, &runVersion, &pendingCommand, &lockedFence, &cancelRequested, &dueAt)
	if err != nil {
		return RunClaim{}, err
	}
	if status != "queued" || pendingCommand != command.Command.CommandID || lockedFence+1 != candidateFence || cancelRequested != nil || !dueAt.After(now) {
		return RunClaim{}, ErrRunNotClaimable
	}
	nextVersion := runVersion + 1
	tag, err = tx.Exec(ctx, `UPDATE agent.runs SET status='executing',run_version=$1,pending_command_id=NULL,active_command_id=$2,active_attempt_id=$3,current_fence=$4,lease_token_hash=$5,lease_expires_at=$6,updated_at=$7 WHERE id=$8 AND tenant_id=$9 AND status='queued' AND run_version=$10 AND pending_command_id=$2 AND current_fence=$11 AND cancel_requested_at IS NULL AND due_at>$7`, nextVersion, command.Command.CommandID, attemptID, candidateFence, credential.Digest[:], expiresAt, now, command.Command.AggregateID, command.Command.TenantID, runVersion, lockedFence)
	if err != nil || tag.RowsAffected() != 1 {
		return RunClaim{}, ErrRunNotClaimable
	}
	var jobID string
	err = tx.QueryRow(ctx, `UPDATE agent.jobs SET status='running',dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending' AND available_at<=$1 AND (due_at IS NULL OR due_at>$1) RETURNING id::text`, now, command.Command.TenantID, command.Command.CommandID).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunClaim{}, ErrRunNotClaimable
	}
	if err != nil {
		return RunClaim{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent.job_attempts (id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'running',$9,$9,$9)`, attemptID, command.Command.TenantID, jobID, command.Command.CommandID, candidateFence, credential.Digest[:], expiresAt, command.WorkerID, now)
	if err != nil {
		return RunClaim{}, err
	}
	eventIDs, err := store.claimEventIdentifiers(attemptID)
	if err != nil {
		return RunClaim{}, err
	}
	started := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.runEvent, TenantID: command.Command.TenantID, UserID: userID, EventType: claimRunEventType(command.Command.CommandType), SchemaVersion: 1, AggregateKind: "run", AggregateID: command.Command.AggregateID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.RunEvent.Ref, PayloadHash: command.RunEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.runOutbox, CommandID: eventIDs.runPublish, CommandType: "events.publish", PayloadRef: command.RunEvent.Ref, PayloadHash: command.RunEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, started); err != nil {
		return RunClaim{}, err
	}
	causationID := eventIDs.runEvent
	attemptStarted := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: userID, EventType: "JobAttemptStarted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: attemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.attemptOutbox, CommandID: eventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptStarted); err != nil {
		return RunClaim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RunClaim{}, err
	}
	return RunClaim{RunID: command.Command.AggregateID, TenantID: command.Command.TenantID, UserID: userID, StoreEpoch: command.Command.StoreEpoch, RunVersion: nextVersion, CommandID: command.Command.CommandID, ConsumerName: command.ConsumerName, RequestHash: command.Command.PayloadHash, JobID: jobID, InboxID: inboxID, AttemptID: attemptID, Fence: candidateFence, LeaseToken: credential.Raw, LeaseExpiresAt: expiresAt}, nil
}

type claimEventIDs struct{ runEvent, runOutbox, runPublish, attemptEvent, attemptOutbox, attemptPublish string }

func (store RunStore) claimEventIdentifiers(attemptID string) (claimEventIDs, error) {
	domains := []string{"run-started-event", "run-started-publish-outbox", "run-started-publish-command", "job-attempt-started-event", "job-attempt-started-publish-outbox", "job-attempt-started-publish-command"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, attemptID)
		if err != nil {
			return claimEventIDs{}, err
		}
		values[index] = value
	}
	return claimEventIDs{values[0], values[1], values[2], values[3], values[4], values[5]}, nil
}

func (store RunStore) validClaim() bool {
	return store.valid() && store.Epochs != nil && store.LeaseTTL > 0 && store.Tokens.Purpose != "" && len(store.Tokens.Pepper) >= 32
}

func validClaimRun(command ClaimRunCommand) bool {
	delivered := command.Command
	return delivered.TenantID != "" && delivered.StoreEpoch != "" && delivered.CommandID != "" && validRunCommandType(delivered.CommandType) && delivered.AggregateKind == "run" && delivered.AggregateID != "" && delivered.PayloadRef != "" && delivered.PayloadHash != "" && command.ConsumerName != "" && command.WorkerID != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.RunEvent) && validPointer(command.AttemptStartedEvent) && validPointer(command.AttemptExpiredEvent)
}

func validRunCommandType(commandType string) bool {
	return commandType == "StartAgentRun" || commandType == "ResumeAgentRun" || commandType == "ResumeParentRun"
}

func claimRunEventType(commandType string) string {
	if commandType == "StartAgentRun" {
		return "RunStarted"
	}
	return "RunResumed"
}
