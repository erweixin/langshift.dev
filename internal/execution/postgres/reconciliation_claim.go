package postgres

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

var ErrReconciliationNotClaimable = errors.New("tool effect reconciliation is not claimable by this command")

type ClaimReconciliationCommand struct {
	Command             eventpostgres.DeliveredCommand
	ConsumerName        string
	WorkerID            string
	Actor               json.RawMessage
	CorrelationID       string
	AttemptStartedEvent PayloadPointer
	AttemptExpiredEvent PayloadPointer
}

type ReconciliationClaim struct {
	ToolCallID, RunID, GroupID, EffectID, TenantID, UserID, StoreEpoch string
	ToolCallVersion, EffectVersion                                     uint64
	EffectClass, EffectScope, EffectKey, ProviderID, ProviderRequestID string
	CommandID, ConsumerName, RequestHash, JobID, InboxID, AttemptID    string
	Fence                                                              uint64
	LeaseToken                                                         string
	LeaseExpiresAt                                                     time.Time
	Completed                                                          bool
}

// ClaimReconciliation installs an execution right for a provider-side lookup.
// It never reinstalls a ToolCall execution lease or changes effect ownership.
func (store RunStore) ClaimReconciliation(ctx context.Context, command ClaimReconciliationCommand) (ReconciliationClaim, error) {
	if !store.validClaim() {
		return ReconciliationClaim{}, ErrConfiguration
	}
	if !validClaimReconciliation(command) {
		return ReconciliationClaim{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, command.Command.StoreEpoch); err != nil {
		return ReconciliationClaim{}, err
	}
	random := store.Random
	if random == nil {
		random = rand.Reader
	}
	inboxID, err := ids.NewUUIDFrom(random)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	attemptID, err := ids.NewUUIDFrom(random)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	manager := store.Tokens
	manager.Random = random
	credential, err := manager.Issue()
	if err != nil {
		return ReconciliationClaim{}, err
	}
	now := store.claimNow()
	expiresAt := now.Add(store.LeaseTTL).UTC().Truncate(time.Microsecond)
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ReconciliationClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.Command.TenantID); err != nil {
		return ReconciliationClaim{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.inbox(id,tenant_id,store_epoch,consumer_name,command_id,status,owner_attempt_id,fence,lease_token_hash,lease_expires_at,request_hash) VALUES($1,$2,$3,$4,$5,'running',$6,1,$7,$8,$9) ON CONFLICT DO NOTHING`, inboxID, command.Command.TenantID, command.Command.StoreEpoch, command.ConsumerName, command.Command.CommandID, attemptID, credential.Digest[:], expiresAt, command.Command.PayloadHash)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	if tag.RowsAffected() == 0 {
		var actualInboxID, actualEpoch, status, actualAttempt, requestHash string
		var actualFence uint64
		var actualExpiry time.Time
		var actualDigest []byte
		err = tx.QueryRow(ctx, `SELECT id::text,store_epoch::text,status,owner_attempt_id::text,fence,lease_token_hash,lease_expires_at,request_hash FROM agent.inbox WHERE tenant_id=$1 AND consumer_name=$2 AND command_id=$3 FOR UPDATE`, command.Command.TenantID, command.ConsumerName, command.Command.CommandID).Scan(&actualInboxID, &actualEpoch, &status, &actualAttempt, &actualFence, &actualDigest, &actualExpiry, &requestHash)
		if err != nil {
			return ReconciliationClaim{}, err
		}
		base := ReconciliationClaim{ToolCallID: command.Command.AggregateID, TenantID: command.Command.TenantID, StoreEpoch: command.Command.StoreEpoch, CommandID: command.Command.CommandID, ConsumerName: command.ConsumerName, RequestHash: command.Command.PayloadHash, InboxID: actualInboxID, AttemptID: actualAttempt, Fence: actualFence, LeaseExpiresAt: actualExpiry}
		if actualEpoch != command.Command.StoreEpoch || requestHash != command.Command.PayloadHash {
			return ReconciliationClaim{}, ErrClaimConflict
		}
		if status == "completed" {
			base.Completed = true
			return base, ErrClaimCompleted
		}
		if status == "running" && actualExpiry.After(now) {
			return base, ErrClaimBusy
		}
		if status == "running" {
			claim, reclaimErr := store.reclaimExpiredReconciliation(ctx, tx, command, expiredReconciliationClaim{inboxID: actualInboxID, oldAttemptID: actualAttempt, newAttemptID: attemptID, oldFence: actualFence, newFence: actualFence + 1, oldDigest: actualDigest, newDigest: credential.Digest[:], oldExpiry: actualExpiry, newExpiry: expiresAt, now: now, newToken: credential.Raw})
			if reclaimErr != nil {
				return ReconciliationClaim{}, reclaimErr
			}
			if err = tx.Commit(ctx); err != nil {
				return ReconciliationClaim{}, err
			}
			return claim, nil
		}
		return ReconciliationClaim{}, ErrReconciliationNotClaimable
	}

	claim, err := store.lockReconciliationTarget(ctx, tx, command.Command.TenantID, command.Command.AggregateID, now)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	claim.StoreEpoch, claim.CommandID, claim.ConsumerName, claim.RequestHash = command.Command.StoreEpoch, command.Command.CommandID, command.ConsumerName, command.Command.PayloadHash
	claim.InboxID, claim.AttemptID, claim.Fence, claim.LeaseToken, claim.LeaseExpiresAt = inboxID, attemptID, 1, credential.Raw, expiresAt
	err = tx.QueryRow(ctx, `UPDATE agent.jobs SET status='running',dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending' AND available_at<=$1 AND (due_at IS NULL OR due_at>$1) RETURNING id::text`, now, command.Command.TenantID, command.Command.CommandID).Scan(&claim.JobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReconciliationClaim{}, ErrReconciliationNotClaimable
	}
	if err != nil {
		return ReconciliationClaim{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES($1,$2,$3,$4,1,$5,$6,$7,'running',$8,$8,$8)`, attemptID, command.Command.TenantID, claim.JobID, command.Command.CommandID, credential.Digest[:], expiresAt, command.WorkerID, now); err != nil {
		return ReconciliationClaim{}, err
	}
	eventIDs, err := store.reconciliationClaimEventIdentifiers(attemptID)
	if err != nil {
		return ReconciliationClaim{}, err
	}
	causationID := command.Command.CommandID
	started := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: claim.TenantID, UserID: claim.UserID, EventType: "JobAttemptStarted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: attemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, started); err != nil {
		return ReconciliationClaim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ReconciliationClaim{}, err
	}
	return claim, nil
}

func (store RunStore) lockReconciliationTarget(ctx context.Context, tx pgx.Tx, tenantID, toolCallID string, now time.Time) (ReconciliationClaim, error) {
	var claim ReconciliationClaim
	var effectStatus string
	var reconciliationDueAt time.Time
	err := tx.QueryRow(ctx, `SELECT id::text,run_id::text,effect_class,effect_scope,effect_key,provider_id,provider_request_id,version,status,reconciliation_due_at FROM agent.tool_effects WHERE tenant_id=$1 AND tool_call_id=$2 FOR UPDATE`, tenantID, toolCallID).Scan(&claim.EffectID, &claim.RunID, &claim.EffectClass, &claim.EffectScope, &claim.EffectKey, &claim.ProviderID, &claim.ProviderRequestID, &claim.EffectVersion, &effectStatus, &reconciliationDueAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReconciliationClaim{}, ErrReconciliationNotClaimable
	}
	if err != nil {
		return ReconciliationClaim{}, err
	}
	if effectStatus != "outcome_unknown" || reconciliationDueAt.After(now) {
		return ReconciliationClaim{}, ErrReconciliationNotClaimable
	}
	var toolStatus, toolEffectClass, groupKind string
	err = tx.QueryRow(ctx, `SELECT t.user_id::text,t.tool_call_version,t.status,t.effect_class,m.group_id::text,g.group_kind FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id JOIN agent.parallel_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id WHERE t.tenant_id=$1 AND t.id=$2 AND t.run_id=$3 FOR UPDATE OF t`, tenantID, toolCallID, claim.RunID).Scan(&claim.UserID, &claim.ToolCallVersion, &toolStatus, &toolEffectClass, &claim.GroupID, &groupKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReconciliationClaim{}, ErrReconciliationNotClaimable
	}
	if err != nil {
		return ReconciliationClaim{}, err
	}
	if toolStatus != string(statemachine.ToolCallOutcomeUnknown) || toolEffectClass != claim.EffectClass || groupKind != "execution" {
		return ReconciliationClaim{}, ErrReconciliationNotClaimable
	}
	claim.ToolCallID, claim.TenantID = toolCallID, tenantID
	return claim, nil
}

type reconciliationClaimEventIDs struct{ event, outbox, publish string }

func (store RunStore) reconciliationClaimEventIdentifiers(attemptID string) (reconciliationClaimEventIDs, error) {
	domains := []string{"reconciliation-attempt-started-event", "reconciliation-attempt-started-publish-outbox", "reconciliation-attempt-started-publish-command"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, attemptID)
		if err != nil {
			return reconciliationClaimEventIDs{}, err
		}
		values[index] = value
	}
	return reconciliationClaimEventIDs{values[0], values[1], values[2]}, nil
}

func validClaimReconciliation(command ClaimReconciliationCommand) bool {
	delivered := command.Command
	return delivered.TenantID != "" && delivered.StoreEpoch != "" && delivered.CommandID != "" && delivered.CommandType == "ReconcileToolEffect" && delivered.AggregateKind == "tool_call" && delivered.AggregateID != "" && delivered.PayloadRef != "" && delivered.PayloadHash != "" && command.ConsumerName != "" && command.WorkerID != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.AttemptStartedEvent) && validPointer(command.AttemptExpiredEvent)
}

func validReconciliationClaim(claim ReconciliationClaim) bool {
	return claim.ToolCallID != "" && claim.RunID != "" && claim.GroupID != "" && claim.EffectID != "" && claim.TenantID != "" && claim.UserID != "" && claim.StoreEpoch != "" && claim.ToolCallVersion > 0 && claim.EffectVersion > 0 && isWriteEffectClass(claim.EffectClass) && claim.EffectScope != "" && claim.EffectKey != "" && claim.ProviderID != "" && claim.ProviderRequestID != "" && claim.CommandID != "" && claim.ConsumerName != "" && claim.RequestHash != "" && claim.JobID != "" && claim.InboxID != "" && claim.AttemptID != "" && claim.Fence > 0 && claim.LeaseToken != "" && !claim.LeaseExpiresAt.IsZero() && !claim.Completed
}
