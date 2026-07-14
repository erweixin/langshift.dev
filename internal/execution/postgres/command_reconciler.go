package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

const (
	RedeliveryReasonClaimSLOElapsed     = "claim_slo_elapsed"
	RedeliveryReasonQueueGenerationLost = "queue_generation_lost"
	maximumRedeliveryTenantPage         = 5000
	maximumRedeliveryCandidatePage      = 500
)

var (
	ErrRedeliveryConfiguration = errors.New("command redelivery configuration is invalid")
	ErrCommandNotRedeliverable = errors.New("command is no longer safely redeliverable")
	ErrCommandHasLiveOwner     = errors.New("command still has a valid execution owner")
	ErrCommandNeedsRepair      = errors.New("command requires reconciliation or repair instead of replay")
	ErrRedeliveryConflict      = errors.New("command redelivery conflicts with durable state")
)

// CommandReconcilerStore restores queue delivery without manufacturing a new
// business command. It only resets the immutable outbox row after proving the
// target still waits for that exact command and no valid Worker owns it.
type CommandReconcilerStore struct {
	Pool         *pgxpool.Pool
	Appender     eventpostgres.Appender
	Epochs       eventpostgres.EpochAuthority
	IDKey        []byte
	ClaimSLO     time.Duration
	BackoffBase  time.Duration
	BackoffLimit time.Duration
	Now          func() time.Time
}

type CommandRedeliveryCandidate struct {
	JobID, TenantID, CommandID, CommandType, AggregateKind, AggregateID string
	PayloadRef, PayloadHash, StoreEpoch                                 string
	JobStatus                                                           string
	JobVersion, QueueGeneration, RedeliveryCount                        uint64
	DeliveryDueAt                                                       *time.Time
}

type RequestCommandRedelivery struct {
	TenantID, StoreEpoch, CommandID, ExpectedPayloadHash string
	ExpectedQueueGeneration                              uint64
	ReasonCode                                           string
	Actor                                                json.RawMessage
	CorrelationID                                        string
	RequestedEvent                                       PayloadPointer
}

type CommandRedelivery struct {
	JobID, CommandID, EventID  string
	JobVersion                 uint64
	QueueGeneration            uint64
	RedeliveryCount            uint64
	AvailableAt, DeliveryDueAt time.Time
}

type redeliveryInbox struct {
	id, epoch, status, attemptID, requestHash string
	fence                                     uint64
	leaseExpiresAt                            time.Time
}

type redeliveryTarget struct {
	userID, status                    string
	pendingCommandID, activeCommandID *string
	activeAttemptID                   *string
	leaseExpiresAt                    *time.Time
	effectClass                       string
	reconciliationDueAt               *time.Time
}

type redeliveryJob struct {
	id, status, commandID                                      string
	version, dispatchVersion, queueGeneration, redeliveryCount uint64
	maxAttempts                                                uint64
	availableAt                                                time.Time
	dueAt, deliveryDueAt, dispatchLeaseExpiresAt, requestedAt  *time.Time
	dispatchLeaseHash                                          []byte
}

type redeliveryOutbox struct {
	id, commandType, aggregateKind, aggregateID, epoch, payloadRef, payloadHash, status string
	publishedAt                                                                         *time.Time
}

func (store CommandReconcilerStore) ListReadyTenantIDs(ctx context.Context, storeEpoch, after string, limit, shardIndex, shardCount int) ([]string, error) {
	if !store.valid() || storeEpoch == "" || limit < 1 || limit > maximumRedeliveryTenantPage || shardCount < 1 || shardIndex < 0 || shardIndex >= shardCount {
		return nil, ErrRedeliveryConfiguration
	}
	if err := store.requireEpoch(ctx, storeEpoch); err != nil {
		return nil, err
	}
	now := store.now()
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_command_reconciliation_tenants($1::uuid,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7)`, storeEpoch, after, limit, shardIndex, shardCount, now, now.Add(-store.ClaimSLO))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0, limit)
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (store CommandReconcilerStore) ListCandidates(ctx context.Context, tenantID, storeEpoch, afterCommandID string, limit int) ([]CommandRedeliveryCandidate, error) {
	if !store.valid() || tenantID == "" || storeEpoch == "" || limit < 1 || limit > maximumRedeliveryCandidatePage {
		return nil, ErrRedeliveryConfiguration
	}
	if err := store.requireEpoch(ctx, storeEpoch); err != nil {
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
	now := store.now()
	rows, err := tx.Query(ctx, `SELECT j.id::text,j.tenant_id::text,j.command_id::text,o.command_type,o.aggregate_kind,o.aggregate_id::text,o.payload_ref,o.payload_hash,o.store_epoch::text,j.status,j.version,j.queue_generation,j.redelivery_count,j.delivery_due_at
		FROM agent.jobs j JOIN agent.outbox o ON o.tenant_id=j.tenant_id AND o.command_id=j.command_id
		WHERE j.tenant_id=$1 AND o.store_epoch=$2 AND o.status='published' AND j.status IN ('pending','running') AND j.redelivery_count<j.max_attempts
		  AND j.redelivery_requested_at IS NULL AND (j.dispatch_lease_hash IS NULL OR j.dispatch_lease_expires_at<=$3)
		  AND (NULLIF($4,'')::uuid IS NULL OR j.command_id>NULLIF($4,'')::uuid)
		  AND ((j.delivery_due_at IS NOT NULL AND j.delivery_due_at<=$3) OR (j.delivery_due_at IS NULL AND o.published_at<=$5))
		  AND (j.status='pending' OR EXISTS (SELECT 1 FROM agent.inbox i WHERE i.tenant_id=j.tenant_id AND i.command_id=j.command_id AND i.status='running' AND i.lease_expires_at<=$3))
		ORDER BY j.command_id LIMIT $6`, tenantID, storeEpoch, now, afterCommandID, now.Add(-store.ClaimSLO), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]CommandRedeliveryCandidate, 0, limit)
	for rows.Next() {
		var candidate CommandRedeliveryCandidate
		if err = rows.Scan(&candidate.JobID, &candidate.TenantID, &candidate.CommandID, &candidate.CommandType, &candidate.AggregateKind, &candidate.AggregateID, &candidate.PayloadRef, &candidate.PayloadHash, &candidate.StoreEpoch, &candidate.JobStatus, &candidate.JobVersion, &candidate.QueueGeneration, &candidate.RedeliveryCount, &candidate.DeliveryDueAt); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (store CommandReconcilerStore) RequestRedelivery(ctx context.Context, request RequestCommandRedelivery) (CommandRedelivery, error) {
	if !store.valid() || !validRedeliveryRequest(request) {
		return CommandRedelivery{}, ErrRedeliveryConfiguration
	}
	if err := store.requireEpoch(ctx, request.StoreEpoch); err != nil {
		return CommandRedelivery{}, err
	}
	now := store.now()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CommandRedelivery{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, request.TenantID); err != nil {
		return CommandRedelivery{}, err
	}
	if err = lockCommandDelivery(ctx, tx, request.CommandID); err != nil {
		return CommandRedelivery{}, err
	}

	var observed redeliveryOutbox
	err = tx.QueryRow(ctx, `SELECT id::text,command_type,aggregate_kind,aggregate_id::text,store_epoch::text,payload_ref,payload_hash,status,published_at FROM agent.outbox WHERE tenant_id=$1 AND command_id=$2`, request.TenantID, request.CommandID).Scan(&observed.id, &observed.commandType, &observed.aggregateKind, &observed.aggregateID, &observed.epoch, &observed.payloadRef, &observed.payloadHash, &observed.status, &observed.publishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CommandRedelivery{}, ErrCommandNeedsRepair
	}
	if err != nil {
		return CommandRedelivery{}, err
	}
	if observed.epoch != request.StoreEpoch || observed.payloadHash != request.ExpectedPayloadHash {
		return CommandRedelivery{}, ErrCommandNeedsRepair
	}

	inbox, hasInbox, err := store.lockInbox(ctx, tx, request, now)
	if err != nil {
		return CommandRedelivery{}, err
	}
	target, err := store.lockRedeliveryTarget(ctx, tx, request.TenantID, observed, now)
	if err != nil {
		return CommandRedelivery{}, err
	}
	job, err := store.lockRedeliveryJob(ctx, tx, request.TenantID, request.CommandID)
	if err != nil {
		return CommandRedelivery{}, err
	}
	if err = validateRedeliveryState(observed, target, job, inbox, hasInbox, request, now, store.ClaimSLO); err != nil {
		return CommandRedelivery{}, err
	}
	if hasInbox {
		var attemptStatus string
		var attemptExpiry time.Time
		var attemptFence uint64
		err = tx.QueryRow(ctx, `SELECT status,lease_expires_at,fence FROM agent.job_attempts WHERE tenant_id=$1 AND id=$2 AND job_id=$3 AND command_id=$4 FOR UPDATE`, request.TenantID, inbox.attemptID, job.id, request.CommandID).Scan(&attemptStatus, &attemptExpiry, &attemptFence)
		if err != nil || attemptStatus != "running" || attemptFence != inbox.fence || !attemptExpiry.Equal(inbox.leaseExpiresAt) || attemptExpiry.After(now) {
			return CommandRedelivery{}, ErrCommandNeedsRepair
		}
	}
	var locked redeliveryOutbox
	err = tx.QueryRow(ctx, `SELECT id::text,command_type,aggregate_kind,aggregate_id::text,store_epoch::text,payload_ref,payload_hash,status,published_at FROM agent.outbox WHERE tenant_id=$1 AND command_id=$2 FOR UPDATE`, request.TenantID, request.CommandID).Scan(&locked.id, &locked.commandType, &locked.aggregateKind, &locked.aggregateID, &locked.epoch, &locked.payloadRef, &locked.payloadHash, &locked.status, &locked.publishedAt)
	if err != nil || !sameRedeliveryOutbox(locked, observed) || locked.status != "published" || locked.publishedAt == nil {
		return CommandRedelivery{}, ErrRedeliveryConflict
	}

	nextCount := job.redeliveryCount + 1
	nextGeneration := job.queueGeneration + 1
	nextVersion := job.version + 1
	availableAt := now.Add(store.backoff(nextCount)).UTC().Truncate(time.Microsecond)
	deliveryDueAt := availableAt.Add(store.ClaimSLO).UTC().Truncate(time.Microsecond)
	tag, err := tx.Exec(ctx, `UPDATE agent.jobs SET version=$1,dispatch_version=dispatch_version+1,dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,queue_generation=$2,redelivery_count=$3,redelivery_requested_at=$4,redelivery_reason=$5,delivery_due_at=$6,last_error_code=$5,updated_at=$7 WHERE id=$8 AND tenant_id=$9 AND version=$10 AND command_id=$11 AND queue_generation=$12 AND redelivery_count=$13 AND dispatch_version=$14 AND redelivery_count<max_attempts AND redelivery_requested_at IS NULL AND (dispatch_lease_hash IS NULL OR dispatch_lease_expires_at<=$7)`, nextVersion, nextGeneration, nextCount, availableAt, request.ReasonCode, deliveryDueAt, now, job.id, request.TenantID, job.version, request.CommandID, job.queueGeneration, job.redeliveryCount, job.dispatchVersion)
	if err != nil || tag.RowsAffected() != 1 {
		return CommandRedelivery{}, ErrRedeliveryConflict
	}
	tag, err = tx.Exec(ctx, `UPDATE agent.outbox SET status='pending',available_at=$1,publisher_lease_hash=NULL,publisher_lease_expires_at=NULL,published_at=NULL WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND store_epoch=$5 AND payload_hash=$6 AND status='published'`, availableAt, locked.id, request.TenantID, request.CommandID, request.StoreEpoch, request.ExpectedPayloadHash)
	if err != nil || tag.RowsAffected() != 1 {
		return CommandRedelivery{}, ErrRedeliveryConflict
	}

	eventID, outboxID, publishID, err := store.redeliveryEventIDs(job.id, nextGeneration)
	if err != nil {
		return CommandRedelivery{}, ErrRedeliveryConfiguration
	}
	causationID := request.CommandID
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: request.TenantID, UserID: target.userID, EventType: "CommandRedeliveryRequested", SchemaVersion: 1, AggregateKind: "job", AggregateID: job.id, AggregateVersion: nextVersion, StoreEpoch: request.StoreEpoch, OccurredAt: now, Actor: request.Actor, CausationID: &causationID, CorrelationID: request.CorrelationID, PayloadRef: request.RequestedEvent.Ref, PayloadHash: request.RequestedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: request.RequestedEvent.Ref, PayloadHash: request.RequestedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return CommandRedelivery{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CommandRedelivery{}, err
	}
	return CommandRedelivery{JobID: job.id, CommandID: request.CommandID, EventID: eventID, JobVersion: nextVersion, QueueGeneration: nextGeneration, RedeliveryCount: nextCount, AvailableAt: availableAt, DeliveryDueAt: deliveryDueAt}, nil
}

func (store CommandReconcilerStore) lockInbox(ctx context.Context, tx pgx.Tx, request RequestCommandRedelivery, now time.Time) (redeliveryInbox, bool, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,store_epoch::text,status,owner_attempt_id::text,fence,lease_expires_at,request_hash FROM agent.inbox WHERE tenant_id=$1 AND command_id=$2 FOR UPDATE`, request.TenantID, request.CommandID)
	if err != nil {
		return redeliveryInbox{}, false, err
	}
	defer rows.Close()
	var inbox redeliveryInbox
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return redeliveryInbox{}, false, ErrCommandNeedsRepair
		}
		if err = rows.Scan(&inbox.id, &inbox.epoch, &inbox.status, &inbox.attemptID, &inbox.fence, &inbox.leaseExpiresAt, &inbox.requestHash); err != nil {
			return redeliveryInbox{}, false, err
		}
	}
	if err = rows.Err(); err != nil {
		return redeliveryInbox{}, false, err
	}
	if count == 0 {
		return redeliveryInbox{}, false, nil
	}
	if inbox.epoch != request.StoreEpoch || inbox.requestHash != request.ExpectedPayloadHash {
		return redeliveryInbox{}, false, ErrCommandNeedsRepair
	}
	if inbox.status == "completed" || inbox.status == "abandoned" {
		return redeliveryInbox{}, false, ErrCommandNotRedeliverable
	}
	if inbox.status != "running" {
		return redeliveryInbox{}, false, ErrCommandNeedsRepair
	}
	if inbox.leaseExpiresAt.After(now) {
		return redeliveryInbox{}, false, ErrCommandHasLiveOwner
	}
	return inbox, true, nil
}

func (store CommandReconcilerStore) lockRedeliveryTarget(ctx context.Context, tx pgx.Tx, tenantID string, outbox redeliveryOutbox, now time.Time) (redeliveryTarget, error) {
	var target redeliveryTarget
	switch {
	case outbox.aggregateKind == "run" && validRunCommandType(outbox.commandType):
		err := tx.QueryRow(ctx, `SELECT user_id::text,status,pending_command_id::text,active_command_id::text,active_attempt_id::text,lease_expires_at FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, outbox.aggregateID).Scan(&target.userID, &target.status, &target.pendingCommandID, &target.activeCommandID, &target.activeAttemptID, &target.leaseExpiresAt)
		if err != nil {
			return redeliveryTarget{}, ErrCommandNeedsRepair
		}
	case outbox.aggregateKind == "tool_call" && outbox.commandType == "ExecuteToolCall":
		err := tx.QueryRow(ctx, `SELECT user_id::text,status,pending_command_id::text,active_command_id::text,active_attempt_id::text,lease_expires_at,effect_class FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, outbox.aggregateID).Scan(&target.userID, &target.status, &target.pendingCommandID, &target.activeCommandID, &target.activeAttemptID, &target.leaseExpiresAt, &target.effectClass)
		if err != nil {
			return redeliveryTarget{}, ErrCommandNeedsRepair
		}
	case outbox.aggregateKind == "tool_call" && outbox.commandType == "ReconcileToolEffect":
		var effectStatus string
		err := tx.QueryRow(ctx, `SELECT t.user_id::text,t.status,e.status,e.reconciliation_due_at FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE t.tenant_id=$1 AND t.id=$2 FOR UPDATE OF t,e`, tenantID, outbox.aggregateID).Scan(&target.userID, &target.status, &effectStatus, &target.reconciliationDueAt)
		if err != nil || target.status != "outcome_unknown" || effectStatus != "outcome_unknown" || target.reconciliationDueAt == nil || target.reconciliationDueAt.After(now) {
			return redeliveryTarget{}, ErrCommandNotRedeliverable
		}
	default:
		return redeliveryTarget{}, ErrCommandNeedsRepair
	}
	return target, nil
}

func (store CommandReconcilerStore) lockRedeliveryJob(ctx context.Context, tx pgx.Tx, tenantID, commandID string) (redeliveryJob, error) {
	var job redeliveryJob
	err := tx.QueryRow(ctx, `SELECT id::text,status,command_id::text,version,dispatch_version,queue_generation,redelivery_count,max_attempts,available_at,due_at,delivery_due_at,dispatch_lease_hash,dispatch_lease_expires_at,redelivery_requested_at FROM agent.jobs WHERE tenant_id=$1 AND command_id=$2 FOR UPDATE`, tenantID, commandID).Scan(&job.id, &job.status, &job.commandID, &job.version, &job.dispatchVersion, &job.queueGeneration, &job.redeliveryCount, &job.maxAttempts, &job.availableAt, &job.dueAt, &job.deliveryDueAt, &job.dispatchLeaseHash, &job.dispatchLeaseExpiresAt, &job.requestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return redeliveryJob{}, ErrCommandNeedsRepair
	}
	return job, err
}

func validateRedeliveryState(outbox redeliveryOutbox, target redeliveryTarget, job redeliveryJob, inbox redeliveryInbox, hasInbox bool, request RequestCommandRedelivery, now time.Time, claimSLO time.Duration) error {
	if outbox.status != "published" || outbox.publishedAt == nil || job.commandID != request.CommandID || job.requestedAt != nil || request.ExpectedQueueGeneration != job.queueGeneration {
		return ErrCommandNotRedeliverable
	}
	if job.redeliveryCount >= job.maxAttempts || job.queueGeneration != job.redeliveryCount+1 {
		return ErrCommandNeedsRepair
	}
	if job.dispatchLeaseExpiresAt != nil && job.dispatchLeaseExpiresAt.After(now) {
		return ErrCommandHasLiveOwner
	}
	if job.dueAt != nil && !job.dueAt.After(now) {
		return ErrCommandNotRedeliverable
	}
	if request.ReasonCode == RedeliveryReasonClaimSLOElapsed {
		deliveryDue := outbox.publishedAt.Add(claimSLO)
		if job.deliveryDueAt != nil {
			deliveryDue = *job.deliveryDueAt
		}
		if deliveryDue.After(now) {
			return ErrCommandNotRedeliverable
		}
	}
	if job.status == "pending" {
		if hasInbox {
			return ErrCommandNeedsRepair
		}
		switch outbox.commandType {
		case "StartAgentRun", "ResumeAgentRun", "ResumeParentRun":
			if target.status != "queued" || target.pendingCommandID == nil || *target.pendingCommandID != request.CommandID {
				return ErrCommandNotRedeliverable
			}
		case "ExecuteToolCall":
			if target.status != "requested" || target.pendingCommandID == nil || *target.pendingCommandID != request.CommandID {
				return ErrCommandNotRedeliverable
			}
		case "ReconcileToolEffect":
			// The effect lock in lockRedeliveryTarget is the pending-command guard.
		default:
			return ErrCommandNeedsRepair
		}
		return nil
	}
	if job.status != "running" || !hasInbox {
		return ErrCommandNotRedeliverable
	}
	switch outbox.commandType {
	case "StartAgentRun", "ResumeAgentRun", "ResumeParentRun":
		if target.status != "executing" || target.activeCommandID == nil || *target.activeCommandID != request.CommandID || target.activeAttemptID == nil || *target.activeAttemptID != inbox.attemptID || target.leaseExpiresAt == nil || !target.leaseExpiresAt.Equal(inbox.leaseExpiresAt) || target.leaseExpiresAt.After(now) {
			return ErrCommandNotRedeliverable
		}
	case "ExecuteToolCall":
		if target.status != "executing" || target.activeCommandID == nil || *target.activeCommandID != request.CommandID || target.activeAttemptID == nil || *target.activeAttemptID != inbox.attemptID || target.leaseExpiresAt == nil || !target.leaseExpiresAt.Equal(inbox.leaseExpiresAt) || target.leaseExpiresAt.After(now) {
			return ErrCommandNotRedeliverable
		}
		if target.effectClass != "read_only" && target.effectClass != "idempotent_write" {
			return ErrCommandNeedsRepair
		}
	case "ReconcileToolEffect":
	default:
		return ErrCommandNeedsRepair
	}
	return nil
}

func validRedeliveryRequest(request RequestCommandRedelivery) bool {
	return request.TenantID != "" && request.StoreEpoch != "" && request.CommandID != "" && request.ExpectedPayloadHash != "" && request.ExpectedQueueGeneration > 0 && (request.ReasonCode == RedeliveryReasonClaimSLOElapsed || request.ReasonCode == RedeliveryReasonQueueGenerationLost) && validJSONObject(request.Actor) && request.CorrelationID != "" && validPointer(request.RequestedEvent)
}

func sameRedeliveryOutbox(left, right redeliveryOutbox) bool {
	leftPublished, rightPublished := left.publishedAt, right.publishedAt
	publishedEqual := leftPublished == nil && rightPublished == nil || leftPublished != nil && rightPublished != nil && leftPublished.Equal(*rightPublished)
	return publishedEqual && left.id == right.id && left.commandType == right.commandType && left.aggregateKind == right.aggregateKind && left.aggregateID == right.aggregateID && left.epoch == right.epoch && left.payloadRef == right.payloadRef && left.payloadHash == right.payloadHash && left.status == right.status
}

func (store CommandReconcilerStore) valid() bool {
	return store.Pool != nil && store.Epochs != nil && len(store.IDKey) >= 32 && store.ClaimSLO > 0 && store.BackoffBase > 0 && store.BackoffLimit >= store.BackoffBase
}

func (store CommandReconcilerStore) requireEpoch(ctx context.Context, expected string) error {
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrRedeliveryConfiguration
	}
	if current != expected {
		return ErrStaleEpoch
	}
	return nil
}

func (store CommandReconcilerStore) now() time.Time {
	if store.Now != nil {
		return store.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (store CommandReconcilerStore) backoff(count uint64) time.Duration {
	delay := store.BackoffBase
	for index := uint64(1); index < count && delay < store.BackoffLimit; index++ {
		if delay > store.BackoffLimit/2 {
			return store.BackoffLimit
		}
		delay *= 2
	}
	if delay > store.BackoffLimit {
		return store.BackoffLimit
	}
	return delay
}

func (store CommandReconcilerStore) redeliveryEventIDs(jobID string, generation uint64) (string, string, string, error) {
	seed := fmt.Sprintf("%s:%d", jobID, generation)
	eventID, err := ids.DeterministicUUID(store.IDKey, "command-redelivery-requested-event", seed)
	if err != nil {
		return "", "", "", err
	}
	outboxID, err := ids.DeterministicUUID(store.IDKey, "command-redelivery-requested-publish-outbox", seed)
	if err != nil {
		return "", "", "", err
	}
	publishID, err := ids.DeterministicUUID(store.IDKey, "command-redelivery-requested-publish-command", seed)
	return eventID, outboxID, publishID, err
}
