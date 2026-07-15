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

var ErrPreviewNotClaimable = errors.New("tool preview is not claimable by this command")

type ClaimToolPreviewCommand struct {
	Command             eventpostgres.DeliveredCommand
	ConsumerName        string
	WorkerID            string
	Actor               json.RawMessage
	CorrelationID       string
	PreviewStartedEvent PayloadPointer
	AttemptStartedEvent PayloadPointer
	AttemptExpiredEvent PayloadPointer
}

type PreviewClaim struct {
	ToolCallID, RunID, GroupID, TenantID, UserID, StoreEpoch string
	ToolCallVersion                                          uint64
	EffectID, EffectClass, EffectKey                         string
	CommandID, ConsumerName, RequestHash, JobID, InboxID     string
	AttemptID                                                string
	Fence                                                    uint64
	LeaseToken                                               string
	LeaseExpiresAt                                           time.Time
	Completed                                                bool
}

func (store RunStore) ClaimToolPreview(ctx context.Context, command ClaimToolPreviewCommand) (PreviewClaim, error) {
	if !store.validClaim() {
		return PreviewClaim{}, ErrConfiguration
	}
	if !validClaimToolPreview(command) {
		return PreviewClaim{}, ErrInvalidCommand
	}
	if err := statemachine.ToolCalls.ValidateTransition(statemachine.ToolCallPreviewRequested, statemachine.ToolCallPreparingApproval); err != nil {
		return PreviewClaim{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, command.Command.StoreEpoch); err != nil {
		return PreviewClaim{}, err
	}
	random := store.Random
	if random == nil {
		random = rand.Reader
	}
	inboxID, err := ids.NewUUIDFrom(random)
	if err != nil {
		return PreviewClaim{}, err
	}
	attemptID, err := ids.NewUUIDFrom(random)
	if err != nil {
		return PreviewClaim{}, err
	}
	manager := store.Tokens
	manager.Random = random
	credential, err := manager.Issue()
	if err != nil {
		return PreviewClaim{}, err
	}
	now := store.claimNow()
	expiresAt := now.Add(store.LeaseTTL).UTC().Truncate(time.Microsecond)
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PreviewClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.Command.TenantID); err != nil {
		return PreviewClaim{}, err
	}
	if err = lockScheduledCommandDelivery(ctx, tx, command.Command, store.RequireDispatchFence); err != nil {
		return PreviewClaim{}, err
	}
	var currentFence uint64
	if err = tx.QueryRow(ctx, `SELECT current_fence FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2`, command.Command.TenantID, command.Command.AggregateID).Scan(&currentFence); err != nil {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	candidateFence := currentFence + 1
	tag, err := tx.Exec(ctx, `INSERT INTO agent.inbox(id,tenant_id,store_epoch,consumer_name,command_id,status,owner_attempt_id,fence,lease_token_hash,lease_expires_at,request_hash) VALUES($1,$2,$3,$4,$5,'running',$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, inboxID, command.Command.TenantID, command.Command.StoreEpoch, command.ConsumerName, command.Command.CommandID, attemptID, candidateFence, credential.Digest[:], expiresAt, command.Command.PayloadHash)
	if err != nil {
		return PreviewClaim{}, err
	}
	if tag.RowsAffected() == 0 {
		var actual PreviewClaim
		var status, epoch string
		var digest []byte
		err = tx.QueryRow(ctx, `SELECT id::text,store_epoch::text,status,owner_attempt_id::text,fence,lease_token_hash,lease_expires_at,request_hash FROM agent.inbox WHERE tenant_id=$1 AND consumer_name=$2 AND command_id=$3 FOR UPDATE`, command.Command.TenantID, command.ConsumerName, command.Command.CommandID).Scan(&actual.InboxID, &epoch, &status, &actual.AttemptID, &actual.Fence, &digest, &actual.LeaseExpiresAt, &actual.RequestHash)
		if err != nil || epoch != command.Command.StoreEpoch || actual.RequestHash != command.Command.PayloadHash {
			return PreviewClaim{}, ErrClaimConflict
		}
		actual.ToolCallID, actual.TenantID, actual.StoreEpoch, actual.CommandID, actual.ConsumerName = command.Command.AggregateID, command.Command.TenantID, epoch, command.Command.CommandID, command.ConsumerName
		if status == "completed" {
			actual.Completed = true
			return actual, ErrClaimCompleted
		}
		if status == "running" && actual.LeaseExpiresAt.After(now) {
			return actual, ErrClaimBusy
		}
		if status == "running" {
			result, reclaimErr := store.reclaimExpiredToolPreview(ctx, tx, command, expiredPreviewClaim{inboxID: actual.InboxID, oldAttemptID: actual.AttemptID, newAttemptID: attemptID, oldFence: actual.Fence, newFence: candidateFence, oldDigest: digest, newDigest: credential.Digest[:], oldExpiry: actual.LeaseExpiresAt, newExpiry: expiresAt, now: now, newToken: credential.Raw})
			if reclaimErr != nil {
				return PreviewClaim{}, reclaimErr
			}
			if err = tx.Commit(ctx); err != nil {
				return PreviewClaim{}, err
			}
			return result, nil
		}
		return actual, ErrPreviewNotClaimable
	}
	var claim PreviewClaim
	var status, pendingCommand, groupKind, effectStatus string
	err = tx.QueryRow(ctx, `SELECT t.user_id::text,t.run_id::text,m.group_id::text,g.group_kind,t.status,t.pending_command_id::text,t.tool_call_version,t.current_fence,t.effect_class,COALESCE(t.effect_key,''),COALESCE(e.id::text,''),COALESCE(e.status,'') FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id JOIN agent.parallel_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id LEFT JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE t.tenant_id=$1 AND t.id=$2 FOR UPDATE OF t`, command.Command.TenantID, command.Command.AggregateID).Scan(&claim.UserID, &claim.RunID, &claim.GroupID, &groupKind, &status, &pendingCommand, &claim.ToolCallVersion, &currentFence, &claim.EffectClass, &claim.EffectKey, &claim.EffectID, &effectStatus)
	if err != nil || groupKind != "approval_preview" || status != "preview_requested" || pendingCommand != command.Command.CommandID || currentFence+1 != candidateFence || claim.EffectClass == "read_only" || claim.EffectID == "" || effectStatus != "prepared" {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	var runStatus string
	var dueAt time.Time
	var cancelRequested *time.Time
	if err = tx.QueryRow(ctx, `SELECT status,due_at,cancel_requested_at FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.Command.TenantID, claim.RunID).Scan(&runStatus, &dueAt, &cancelRequested); err != nil || runStatus != "waiting_tool" || cancelRequested != nil || !dueAt.After(now) {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	nextVersion := claim.ToolCallVersion + 1
	tag, err = tx.Exec(ctx, `UPDATE agent.tool_calls SET status='preparing_approval',tool_call_version=$1,pending_command_id=NULL,active_command_id=$2,active_attempt_id=$3,current_fence=$4,lease_token_hash=$5,lease_expires_at=$6,updated_at=$7 WHERE tenant_id=$8 AND id=$9 AND status='preview_requested' AND tool_call_version=$10 AND pending_command_id=$2 AND current_fence=$11`, nextVersion, command.Command.CommandID, attemptID, candidateFence, credential.Digest[:], expiresAt, now, command.Command.TenantID, command.Command.AggregateID, claim.ToolCallVersion, currentFence)
	if err != nil || tag.RowsAffected() != 1 {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	err = tx.QueryRow(ctx, `UPDATE agent.jobs SET status='running',dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending' AND available_at<=$1 AND (due_at IS NULL OR due_at>$1) RETURNING id::text`, now, command.Command.TenantID, command.Command.CommandID).Scan(&claim.JobID)
	if err != nil {
		return PreviewClaim{}, ErrPreviewNotClaimable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'running',$9,$9,$9)`, attemptID, command.Command.TenantID, claim.JobID, command.Command.CommandID, candidateFence, credential.Digest[:], expiresAt, command.WorkerID, now); err != nil {
		return PreviewClaim{}, err
	}
	eventIDs, err := store.previewClaimEventIDs(attemptID)
	if err != nil {
		return PreviewClaim{}, err
	}
	previewEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.previewEvent, TenantID: command.Command.TenantID, UserID: claim.UserID, EventType: "ToolCallPreviewStarted", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: command.Command.AggregateID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.PreviewStartedEvent.Ref, PayloadHash: command.PreviewStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.previewOutbox, CommandID: eventIDs.previewPublish, CommandType: "events.publish", PayloadRef: command.PreviewStartedEvent.Ref, PayloadHash: command.PreviewStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, previewEvent); err != nil {
		return PreviewClaim{}, err
	}
	causationID := eventIDs.previewEvent
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.attemptEvent, TenantID: command.Command.TenantID, UserID: claim.UserID, EventType: "JobAttemptStarted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: attemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.attemptOutbox, CommandID: eventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptStartedEvent.Ref, PayloadHash: command.AttemptStartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return PreviewClaim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PreviewClaim{}, err
	}
	claim.ToolCallID, claim.TenantID, claim.StoreEpoch, claim.ToolCallVersion = command.Command.AggregateID, command.Command.TenantID, command.Command.StoreEpoch, nextVersion
	claim.CommandID, claim.ConsumerName, claim.RequestHash, claim.InboxID, claim.AttemptID = command.Command.CommandID, command.ConsumerName, command.Command.PayloadHash, inboxID, attemptID
	claim.Fence, claim.LeaseToken, claim.LeaseExpiresAt = candidateFence, credential.Raw, expiresAt
	return claim, nil
}

type previewClaimIDs struct{ previewEvent, previewOutbox, previewPublish, attemptEvent, attemptOutbox, attemptPublish string }

func (store RunStore) previewClaimEventIDs(attemptID string) (previewClaimIDs, error) {
	values := make([]string, 6)
	for index, domain := range []string{"tool-preview-started:event", "tool-preview-started:outbox", "tool-preview-started:publish", "tool-preview-attempt:event", "tool-preview-attempt:outbox", "tool-preview-attempt:publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, attemptID)
		if err != nil {
			return previewClaimIDs{}, err
		}
		values[index] = value
	}
	return previewClaimIDs{values[0], values[1], values[2], values[3], values[4], values[5]}, nil
}

func validClaimToolPreview(command ClaimToolPreviewCommand) bool {
	delivered := command.Command
	return delivered.TenantID != "" && delivered.StoreEpoch != "" && delivered.CommandID != "" && delivered.CommandType == "PrepareToolPreview" && delivered.AggregateKind == "tool_call" && delivered.AggregateID != "" && delivered.PayloadRef != "" && delivered.PayloadHash != "" && command.ConsumerName != "" && command.WorkerID != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.PreviewStartedEvent) && validPointer(command.AttemptStartedEvent) && validPointer(command.AttemptExpiredEvent)
}

func validPreviewClaim(claim PreviewClaim) bool {
	return claim.ToolCallID != "" && claim.RunID != "" && claim.GroupID != "" && claim.TenantID != "" && claim.UserID != "" && claim.StoreEpoch != "" && claim.ToolCallVersion > 0 && claim.EffectID != "" && claim.EffectClass != "" && claim.EffectKey != "" && claim.CommandID != "" && claim.ConsumerName != "" && claim.RequestHash != "" && claim.JobID != "" && claim.InboxID != "" && claim.AttemptID != "" && claim.Fence > 0 && claim.LeaseToken != "" && !claim.LeaseExpiresAt.IsZero() && !claim.Completed
}
