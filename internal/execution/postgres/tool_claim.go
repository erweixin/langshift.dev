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

var (
	ErrToolNotClaimable              = errors.New("tool call is not claimable by this command")
	ErrToolEffectNeedsReconciliation = errors.New("tool effect cannot be replayed and requires reconciliation")
)

type ClaimToolCommand struct {
	Command             eventpostgres.DeliveredCommand
	ConsumerName        string
	WorkerID            string
	ExpectedBinding     *ToolBinding
	Actor               json.RawMessage
	CorrelationID       string
	ToolStartedEvent    PayloadPointer
	AttemptStartedEvent PayloadPointer
	AttemptExpiredEvent PayloadPointer
}

// ToolBinding is the immutable execution surface selected by AgentWorker.
// Production ToolWorkers pass it back during claim so command payload
// substitution is rejected before an execution fence is installed.
type ToolBinding struct {
	ToolName, DescriptorSnapshotID, NormalizedInputRef, RequestHash string
	EffectClass, EffectKey, EffectScope, ProviderID                 string
}

type ToolClaim struct {
	ToolCallID, RunID, GroupID, TenantID, UserID, StoreEpoch string
	ToolCallVersion                                          uint64
	EffectID, EffectClass, ProviderRequestID                 string
	CommandID, ConsumerName, RequestHash, JobID, InboxID     string
	AttemptID                                                string
	Fence                                                    uint64
	LeaseToken                                               string
	LeaseExpiresAt                                           time.Time
	Completed                                                bool
	Binding                                                  ToolBinding
}

// ClaimTool installs one fenced execution right for ExecuteToolCall. Inbox
// dedupe is established before the ToolCall row is locked and advanced.
func (store RunStore) ClaimTool(ctx context.Context, command ClaimToolCommand) (ToolClaim, error) {
	if !store.validClaim() {
		return ToolClaim{}, ErrConfiguration
	}
	if !validClaimTool(command) {
		return ToolClaim{}, ErrInvalidCommand
	}
	if err := statemachine.ToolCalls.ValidateTransition(statemachine.ToolCallRequested, statemachine.ToolCallExecuting); err != nil {
		return ToolClaim{}, ErrInvalidCommand
	}
	currentEpoch, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || currentEpoch == "" {
		return ToolClaim{}, ErrConfiguration
	}
	if currentEpoch != command.Command.StoreEpoch || currentEpoch != store.StoreEpoch {
		return ToolClaim{}, ErrStaleEpoch
	}
	random := store.Random
	if random == nil {
		random = rand.Reader
	}
	inboxID, err := ids.NewUUIDFrom(random)
	if err != nil {
		return ToolClaim{}, err
	}
	attemptID, err := ids.NewUUIDFrom(random)
	if err != nil {
		return ToolClaim{}, err
	}
	manager := store.Tokens
	manager.Random = random
	credential, err := manager.Issue()
	if err != nil {
		return ToolClaim{}, err
	}
	now := store.claimNow()
	expiresAt := now.Add(store.LeaseTTL).UTC().Truncate(time.Microsecond)
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ToolClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.Command.TenantID); err != nil {
		return ToolClaim{}, err
	}
	if err = lockScheduledCommandDelivery(ctx, tx, command.Command, store.RequireDispatchFence); err != nil {
		return ToolClaim{}, err
	}
	var currentFence uint64
	err = tx.QueryRow(ctx, `SELECT current_fence FROM agent.tool_calls WHERE id=$1 AND tenant_id=$2`, command.Command.AggregateID, command.Command.TenantID).Scan(&currentFence)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolClaim{}, ErrToolNotClaimable
	}
	if err != nil {
		return ToolClaim{}, err
	}
	candidateFence := currentFence + 1
	tag, err := tx.Exec(ctx, `INSERT INTO agent.inbox(id,tenant_id,store_epoch,consumer_name,command_id,status,owner_attempt_id,fence,lease_token_hash,lease_expires_at,request_hash) VALUES($1,$2,$3,$4,$5,'running',$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, inboxID, command.Command.TenantID, command.Command.StoreEpoch, command.ConsumerName, command.Command.CommandID, attemptID, candidateFence, credential.Digest[:], expiresAt, command.Command.PayloadHash)
	if err != nil {
		return ToolClaim{}, err
	}
	if tag.RowsAffected() == 0 {
		var actualInboxID, actualEpoch, status, actualAttempt, requestHash string
		var actualFence uint64
		var actualExpiry time.Time
		var actualDigest []byte
		err = tx.QueryRow(ctx, `SELECT id::text,store_epoch::text,status,owner_attempt_id::text,fence,lease_token_hash,lease_expires_at,request_hash FROM agent.inbox WHERE tenant_id=$1 AND consumer_name=$2 AND command_id=$3 FOR UPDATE`, command.Command.TenantID, command.ConsumerName, command.Command.CommandID).Scan(&actualInboxID, &actualEpoch, &status, &actualAttempt, &actualFence, &actualDigest, &actualExpiry, &requestHash)
		if err != nil {
			return ToolClaim{}, err
		}
		if actualEpoch != command.Command.StoreEpoch || requestHash != command.Command.PayloadHash {
			return ToolClaim{}, ErrClaimConflict
		}
		base := ToolClaim{ToolCallID: command.Command.AggregateID, TenantID: command.Command.TenantID, StoreEpoch: command.Command.StoreEpoch, CommandID: command.Command.CommandID, ConsumerName: command.ConsumerName, RequestHash: command.Command.PayloadHash, InboxID: actualInboxID, AttemptID: actualAttempt, Fence: actualFence, LeaseExpiresAt: actualExpiry}
		if status == "completed" {
			base.Completed = true
			return base, ErrClaimCompleted
		}
		if status == "running" && actualExpiry.After(now) {
			return base, ErrClaimBusy
		}
		if status == "running" {
			result, reclaimErr := store.reclaimExpiredTool(ctx, tx, command, expiredToolClaim{inboxID: actualInboxID, oldAttemptID: actualAttempt, newAttemptID: attemptID, oldFence: actualFence, newFence: candidateFence, oldDigest: actualDigest, newDigest: credential.Digest[:], oldExpiry: actualExpiry, newExpiry: expiresAt, now: now, newToken: credential.Raw})
			if reclaimErr != nil {
				return ToolClaim{}, reclaimErr
			}
			if err = tx.Commit(ctx); err != nil {
				return ToolClaim{}, err
			}
			return result, nil
		}
		return ToolClaim{}, ErrToolNotClaimable
	}

	var effectID, ledgerEffectClass, effectStatus, ledgerEffectKey, effectScope, providerID string
	var effectVersion uint64
	effectErr := tx.QueryRow(ctx, `SELECT id::text,effect_class,status,version,effect_key,effect_scope,provider_id FROM agent.tool_effects WHERE tenant_id=$1 AND tool_call_id=$2 FOR UPDATE`, command.Command.TenantID, command.Command.AggregateID).Scan(&effectID, &ledgerEffectClass, &effectStatus, &effectVersion, &ledgerEffectKey, &effectScope, &providerID)
	hasEffect := effectErr == nil
	if effectErr != nil && !errors.Is(effectErr, pgx.ErrNoRows) {
		return ToolClaim{}, effectErr
	}
	var userID, runID, groupID, status, pendingCommand, toolName, descriptorSnapshotID, normalizedInputRef, toolRequestHash, toolEffectClass, toolEffectKey string
	var toolVersion, lockedFence uint64
	err = tx.QueryRow(ctx, `SELECT t.user_id::text,t.run_id::text,m.group_id::text,t.status,t.pending_command_id::text,t.tool_call_version,t.current_fence,t.tool_name,t.descriptor_snapshot_id,t.normalized_input_ref,t.request_hash,t.effect_class,COALESCE(t.effect_key,'') FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id WHERE t.id=$1 AND t.tenant_id=$2 FOR UPDATE OF t`, command.Command.AggregateID, command.Command.TenantID).Scan(&userID, &runID, &groupID, &status, &pendingCommand, &toolVersion, &lockedFence, &toolName, &descriptorSnapshotID, &normalizedInputRef, &toolRequestHash, &toolEffectClass, &toolEffectKey)
	if err != nil {
		return ToolClaim{}, ErrToolNotClaimable
	}
	binding := ToolBinding{ToolName: toolName, DescriptorSnapshotID: descriptorSnapshotID, NormalizedInputRef: normalizedInputRef, RequestHash: toolRequestHash, EffectClass: toolEffectClass, EffectKey: toolEffectKey, EffectScope: effectScope, ProviderID: providerID}
	if status != string(statemachine.ToolCallRequested) || pendingCommand != command.Command.CommandID || lockedFence+1 != candidateFence || hasEffect != (toolEffectClass != "read_only") || hasEffect && (effectStatus != "prepared" || ledgerEffectClass != toolEffectClass || ledgerEffectKey != toolEffectKey) || command.ExpectedBinding != nil && *command.ExpectedBinding != binding {
		return ToolClaim{}, ErrToolNotClaimable
	}
	var runStatus string
	var cancelRequested *time.Time
	var dueAt time.Time
	if err = tx.QueryRow(ctx, `SELECT status,cancel_requested_at,due_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, runID, command.Command.TenantID).Scan(&runStatus, &cancelRequested, &dueAt); err != nil || runStatus != string(statemachine.RunWaitingTool) || cancelRequested != nil || !dueAt.After(now) {
		return ToolClaim{}, ErrToolNotClaimable
	}
	nextVersion := toolVersion + 1
	tag, err = tx.Exec(ctx, `UPDATE agent.tool_calls SET status='executing',tool_call_version=$1,pending_command_id=NULL,active_command_id=$2,active_attempt_id=$3,current_fence=$4,lease_token_hash=$5,lease_expires_at=$6,updated_at=$7 WHERE id=$8 AND tenant_id=$9 AND status='requested' AND tool_call_version=$10 AND pending_command_id=$2 AND current_fence=$11`, nextVersion, command.Command.CommandID, attemptID, candidateFence, credential.Digest[:], expiresAt, now, command.Command.AggregateID, command.Command.TenantID, toolVersion, lockedFence)
	if err != nil || tag.RowsAffected() != 1 {
		return ToolClaim{}, ErrToolNotClaimable
	}
	providerRequestID := ""
	if hasEffect {
		providerRequestID = effectID
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=$1,status='executing',provider_request_id=$2,execution_attempt_id=$3,execution_fence=$4,updated_at=$5 WHERE id=$6 AND tenant_id=$7 AND version=$8 AND status='prepared' AND effect_class=$9`, effectVersion+1, providerRequestID, attemptID, candidateFence, now, effectID, command.Command.TenantID, effectVersion, toolEffectClass); updateErr != nil || tag.RowsAffected() != 1 {
			return ToolClaim{}, ErrToolNotClaimable
		}
	}
	var jobID string
	err = tx.QueryRow(ctx, `UPDATE agent.jobs SET status='running',dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending' AND available_at<=$1 AND (due_at IS NULL OR due_at>$1) RETURNING id::text`, now, command.Command.TenantID, command.Command.CommandID).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolClaim{}, ErrToolNotClaimable
	}
	if err != nil {
		return ToolClaim{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'running',$9,$9,$9)`, attemptID, command.Command.TenantID, jobID, command.Command.CommandID, candidateFence, credential.Digest[:], expiresAt, command.WorkerID, now); err != nil {
		return ToolClaim{}, err
	}
	eventIDs, err := store.toolClaimEventIdentifiers(attemptID)
	if err != nil {
		return ToolClaim{}, err
	}
	started := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.toolEvent, TenantID: command.Command.TenantID, UserID: userID, EventType: "ToolCallStarted", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: command.Command.AggregateID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.ToolStartedEvent.Ref, PayloadHash: command.ToolStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.toolOutbox, CommandID: eventIDs.toolPublish, CommandType: "events.publish", PayloadRef: command.ToolStartedEvent.Ref, PayloadHash: command.ToolStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, started); err != nil {
		return ToolClaim{}, err
	}
	causationID := eventIDs.toolEvent
	attemptStarted := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: userID, EventType: "JobAttemptStarted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: attemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.attemptOutbox, CommandID: eventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptStarted); err != nil {
		return ToolClaim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ToolClaim{}, err
	}
	return ToolClaim{ToolCallID: command.Command.AggregateID, RunID: runID, GroupID: groupID, TenantID: command.Command.TenantID, UserID: userID, StoreEpoch: command.Command.StoreEpoch, ToolCallVersion: nextVersion, EffectID: effectID, EffectClass: toolEffectClass, ProviderRequestID: providerRequestID, CommandID: command.Command.CommandID, ConsumerName: command.ConsumerName, RequestHash: command.Command.PayloadHash, JobID: jobID, InboxID: inboxID, AttemptID: attemptID, Fence: candidateFence, LeaseToken: credential.Raw, LeaseExpiresAt: expiresAt, Binding: binding}, nil
}

type toolClaimEventIDs struct{ toolEvent, toolOutbox, toolPublish, attemptEvent, attemptOutbox, attemptPublish string }

func (store RunStore) toolClaimEventIdentifiers(attemptID string) (toolClaimEventIDs, error) {
	domains := []string{"tool-call-started-event", "tool-call-started-publish-outbox", "tool-call-started-publish-command", "tool-attempt-started-event", "tool-attempt-started-publish-outbox", "tool-attempt-started-publish-command"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, attemptID)
		if err != nil {
			return toolClaimEventIDs{}, err
		}
		values[index] = value
	}
	return toolClaimEventIDs{values[0], values[1], values[2], values[3], values[4], values[5]}, nil
}

func validClaimTool(command ClaimToolCommand) bool {
	delivered := command.Command
	return delivered.TenantID != "" && delivered.StoreEpoch != "" && delivered.CommandID != "" && delivered.CommandType == "ExecuteToolCall" && delivered.AggregateKind == "tool_call" && delivered.AggregateID != "" && delivered.PayloadRef != "" && delivered.PayloadHash != "" && command.ConsumerName != "" && command.WorkerID != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ToolStartedEvent) && validPointer(command.AttemptStartedEvent) && validPointer(command.AttemptExpiredEvent)
}

func validToolClaim(claim ToolClaim) bool {
	effectValid := claim.EffectClass == "read_only" && claim.EffectID == "" && claim.ProviderRequestID == "" || claim.EffectClass != "" && claim.EffectClass != "read_only" && claim.EffectID != "" && claim.ProviderRequestID != ""
	return claim.ToolCallID != "" && claim.RunID != "" && claim.GroupID != "" && claim.TenantID != "" && claim.UserID != "" && claim.StoreEpoch != "" && claim.ToolCallVersion > 0 && effectValid && claim.CommandID != "" && claim.ConsumerName != "" && claim.RequestHash != "" && claim.JobID != "" && claim.InboxID != "" && claim.AttemptID != "" && claim.Fence > 0 && claim.LeaseToken != "" && !claim.LeaseExpiresAt.IsZero() && !claim.Completed
}
