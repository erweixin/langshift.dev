package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

type BeginWorkspacePublishCommand struct {
	RevisionID              string
	ExpectedRevisionVersion uint64
	Claim                   ToolClaim
	Actor                   json.RawMessage
	CorrelationID           string
	StartedEvent            PayloadPointer
}

type WorkspacePublishClaim struct {
	RevisionID, Status, EventID string
	Version, Fence              uint64
	AttemptID                   string
	LeaseExpiresAt              time.Time
	Tool                        ToolClaim
	Replayed                    bool
}

type CompleteWorkspacePublishCommand struct {
	RevisionID              string
	ExpectedRevisionVersion uint64
	Tool                    CompleteToolCommand
	PublishedRevision       string
	PublishedHash           string
	ObservedRevision        string
	CompletionEvent         PayloadPointer
}

type CompletedWorkspacePublish struct {
	RevisionID, Status, EventID string
	Version                     uint64
	PublishedRevision           string
	PublishedHash               string
	ObservedRevision            string
	CompletedAt                 time.Time
	Tool                        CompletedTool
	Replayed                    bool
}

type workspacePublishEventIDs struct{ event, outbox, publish string }

func (store RunStore) BeginWorkspacePublish(ctx context.Context, command BeginWorkspacePublishCommand) (WorkspacePublishClaim, error) {
	claim := command.Claim
	if !store.validClaim() {
		return WorkspacePublishClaim{}, ErrConfiguration
	}
	if !validBeginWorkspacePublish(command) {
		return WorkspacePublishClaim{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return WorkspacePublishClaim{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return WorkspacePublishClaim{}, ErrExecutionRightConflict
	}
	eventIDs, err := store.workspacePublishIDs("started", command.RevisionID, claim.AttemptID)
	if err != nil {
		return WorkspacePublishClaim{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return WorkspacePublishClaim{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return WorkspacePublishClaim{}, err
	}
	var status, toolCallID, commitCommandID, attemptID string
	var version, fence uint64
	var leaseHash []byte
	var leaseExpiresAt *time.Time
	err = tx.QueryRow(ctx, `SELECT status,version,tool_call_id::text,commit_command_id::text,COALESCE(publish_attempt_id::text,''),publish_fence,publish_lease_hash,publish_lease_expires_at FROM agent.workspace_revision_commits WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, claim.TenantID, command.RevisionID).Scan(&status, &version, &toolCallID, &commitCommandID, &attemptID, &fence, &leaseHash, &leaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkspacePublishClaim{}, ErrWorkspaceNotActionable
	}
	if err != nil {
		return WorkspacePublishClaim{}, err
	}
	if status == "publishing" {
		if toolCallID != claim.ToolCallID || commitCommandID != claim.CommandID || attemptID != claim.AttemptID || fence != claim.Fence || !equalBytes(leaseHash, digest[:]) || leaseExpiresAt == nil || !leaseExpiresAt.Equal(claim.LeaseExpiresAt) {
			return WorkspacePublishClaim{}, ErrWorkspaceConflict
		}
		if !leaseExpiresAt.After(now) {
			return WorkspacePublishClaim{}, ErrExecutionRightConflict
		}
		var eventExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='WorkspaceRevisionCommitStarted' AND aggregate_kind='workspace_revision' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5)`, claim.TenantID, eventIDs.event, command.RevisionID, command.ExpectedRevisionVersion+1, store.StoreEpoch).Scan(&eventExists); err != nil {
			return WorkspacePublishClaim{}, err
		}
		if !eventExists {
			return WorkspacePublishClaim{}, ErrWorkspaceConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return WorkspacePublishClaim{}, err
		}
		return WorkspacePublishClaim{RevisionID: command.RevisionID, Status: status, EventID: eventIDs.event, Version: version, Fence: fence, AttemptID: attemptID, LeaseExpiresAt: *leaseExpiresAt, Tool: claim, Replayed: true}, nil
	}
	if status != "authorized" || version != command.ExpectedRevisionVersion || toolCallID != claim.ToolCallID || commitCommandID != claim.CommandID {
		return WorkspacePublishClaim{}, ErrWorkspaceNotActionable
	}
	if err = lockToolInbox(ctx, tx, claim, digest[:], now); err != nil {
		return WorkspacePublishClaim{}, fmt.Errorf("%w: workspace publish inbox", err)
	}
	var lockedTool string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2 AND user_id=$3 AND run_id=$4 AND status='executing' AND tool_call_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$11 FOR UPDATE`, claim.TenantID, claim.ToolCallID, claim.UserID, claim.RunID, claim.ToolCallVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&lockedTool)
	if err != nil || lockedTool != claim.ToolCallID {
		return WorkspacePublishClaim{}, fmt.Errorf("%w: workspace publish tool call", ErrExecutionRightConflict)
	}
	var effectID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agent.tool_effects WHERE tenant_id=$1 AND id=$2 AND tool_call_id=$3 AND run_id=$4 AND status='executing' AND effect_class='reconcilable_write' AND execution_attempt_id=$5 AND execution_fence=$6 AND provider_request_id=$7 FOR UPDATE`, claim.TenantID, claim.EffectID, claim.ToolCallID, claim.RunID, claim.AttemptID, claim.Fence, claim.ProviderRequestID).Scan(&effectID)
	if err != nil || effectID != claim.EffectID {
		return WorkspacePublishClaim{}, fmt.Errorf("%w: workspace publish effect ledger", ErrExecutionRightConflict)
	}
	nextVersion := version + 1
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_revision_commits SET version=$1,status='publishing',publish_attempt_id=$2,publish_fence=$3,publish_lease_hash=$4,publish_lease_expires_at=$5,publishing_at=$6,updated_at=$6 WHERE tenant_id=$7 AND id=$8 AND version=$9 AND status='authorized' AND commit_command_id=$10`, nextVersion, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now, claim.TenantID, command.RevisionID, version, claim.CommandID)
	if err != nil {
		return WorkspacePublishClaim{}, err
	}
	if tag.RowsAffected() != 1 {
		return WorkspacePublishClaim{}, ErrWorkspaceConflict
	}
	causationID := claim.CommandID
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: claim.TenantID, UserID: claim.UserID, EventType: "WorkspaceRevisionCommitStarted", SchemaVersion: 1, AggregateKind: "workspace_revision", AggregateID: command.RevisionID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.StartedEvent.Ref, PayloadHash: command.StartedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.StartedEvent.Ref, PayloadHash: command.StartedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return WorkspacePublishClaim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return WorkspacePublishClaim{}, err
	}
	return WorkspacePublishClaim{RevisionID: command.RevisionID, Status: "publishing", EventID: eventIDs.event, Version: nextVersion, Fence: claim.Fence, AttemptID: claim.AttemptID, LeaseExpiresAt: claim.LeaseExpiresAt, Tool: claim}, nil
}

func (store RunStore) HeartbeatWorkspacePublish(ctx context.Context, publish WorkspacePublishClaim) (WorkspacePublishClaim, error) {
	if publish.RevisionID == "" || publish.Status != "publishing" || publish.Version < 1 || publish.AttemptID != publish.Tool.AttemptID || publish.Fence != publish.Tool.Fence {
		return WorkspacePublishClaim{}, ErrInvalidCommand
	}
	renewed, err := store.heartbeatTool(ctx, publish.Tool, func(ctx context.Context, tx pgx.Tx, claim ToolClaim, digest []byte, now, expiresAt time.Time) error {
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.workspace_revision_commits SET version=version+1,publish_lease_expires_at=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status='publishing' AND version=$5 AND publish_attempt_id=$6 AND publish_fence=$7 AND publish_lease_hash=$8 AND publish_lease_expires_at=$9`, expiresAt, now, claim.TenantID, publish.RevisionID, publish.Version, claim.AttemptID, claim.Fence, digest, claim.LeaseExpiresAt)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return ErrWorkspaceConflict
		}
		return nil
	})
	if err != nil {
		return WorkspacePublishClaim{}, err
	}
	publish.Version++
	publish.LeaseExpiresAt = renewed.LeaseExpiresAt
	publish.Tool = renewed
	return publish, nil
}

func (store RunStore) CompleteWorkspacePublish(ctx context.Context, command CompleteWorkspacePublishCommand, effect EffectCompletion) (CompletedWorkspacePublish, error) {
	if !store.validClaim() {
		return CompletedWorkspacePublish{}, ErrConfiguration
	}
	if !validCompleteWorkspacePublish(command, effect) || !validCompleteTool(command.Tool) {
		return CompletedWorkspacePublish{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, command.Tool.Claim.StoreEpoch); err != nil {
		return CompletedWorkspacePublish{}, err
	}
	status, eventType := workspaceCompletionState(command.Tool.TargetState)
	eventIDs, err := store.workspacePublishIDs(status, command.RevisionID, command.Tool.Claim.AttemptID)
	if err != nil {
		return CompletedWorkspacePublish{}, err
	}
	if replay, found, replayErr := store.loadCompletedWorkspacePublishReplay(ctx, command, effect, status, eventType, eventIDs.event); replayErr != nil {
		return CompletedWorkspacePublish{}, replayErr
	} else if found {
		return replay, nil
	}
	var completed CompletedWorkspacePublish
	hook := func(ctx context.Context, tx pgx.Tx, toolEventID string, now time.Time) error {
		claim := command.Tool.Claim
		digest, digestErr := store.Tokens.Digest(claim.LeaseToken)
		if digestErr != nil {
			return ErrExecutionRightConflict
		}
		var publishedRevision, publishedHash any
		var confirmedAt, outcomeUnknownAt, reconciliationDueAt, failedAt any
		switch status {
		case "confirmed":
			publishedRevision, publishedHash, confirmedAt = command.PublishedRevision, command.PublishedHash, now
		case "outcome_unknown":
			outcomeUnknownAt, reconciliationDueAt = now, effect.ReconciliationDueAt
		case "failed":
			failedAt = now
		}
		nextVersion := command.ExpectedRevisionVersion + 1
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.workspace_revision_commits SET version=$1,status=$2,published_revision=$3,published_hash=$4,observed_revision=NULLIF($5,''),publish_lease_hash=NULL,publish_lease_expires_at=NULL,confirmed_at=$6,outcome_unknown_at=$7,reconciliation_due_at=$8,failed_at=$9,updated_at=$10 WHERE tenant_id=$11 AND id=$12 AND version=$13 AND status='publishing' AND tool_call_id=$14 AND commit_command_id=$15 AND publish_attempt_id=$16 AND publish_fence=$17 AND publish_lease_hash=$18 AND publish_lease_expires_at=$19`, nextVersion, status, publishedRevision, publishedHash, command.ObservedRevision, confirmedAt, outcomeUnknownAt, reconciliationDueAt, failedAt, now, claim.TenantID, command.RevisionID, command.ExpectedRevisionVersion, claim.ToolCallID, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return ErrWorkspaceConflict
		}
		causationID := toolEventID
		event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: claim.TenantID, UserID: claim.UserID, EventType: eventType, SchemaVersion: 1, AggregateKind: "workspace_revision", AggregateID: command.RevisionID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Tool.Actor, CausationID: &causationID, CorrelationID: command.Tool.CorrelationID, PayloadRef: command.CompletionEvent.Ref, PayloadHash: command.CompletionEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.CompletionEvent.Ref, PayloadHash: command.CompletionEvent.Hash}}}
		if _, appendErr := store.Appender.Append(ctx, tx, event); appendErr != nil {
			return appendErr
		}
		completed = CompletedWorkspacePublish{RevisionID: command.RevisionID, Status: status, EventID: eventIDs.event, Version: nextVersion, PublishedRevision: command.PublishedRevision, PublishedHash: command.PublishedHash, ObservedRevision: command.ObservedRevision, CompletedAt: now}
		return nil
	}
	tool, err := store.completeTool(ctx, command.Tool, &effect, hook)
	if err == nil {
		completed.Tool = tool
		return completed, nil
	}
	if replay, found, replayErr := store.loadCompletedWorkspacePublishReplay(ctx, command, effect, status, eventType, eventIDs.event); replayErr == nil && found {
		return replay, nil
	}
	return CompletedWorkspacePublish{}, err
}

func (store RunStore) loadCompletedWorkspacePublishReplay(ctx context.Context, command CompleteWorkspacePublishCommand, effect EffectCompletion, status, eventType, eventID string) (CompletedWorkspacePublish, bool, error) {
	claim := command.Tool.Claim
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return CompletedWorkspacePublish{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return CompletedWorkspacePublish{}, false, err
	}
	var result CompletedWorkspacePublish
	var attemptID, commitCommandID string
	var fence uint64
	err = tx.QueryRow(ctx, `SELECT id::text,status,version,COALESCE(published_revision,''),COALESCE(published_hash,''),COALESCE(observed_revision,''),updated_at,COALESCE(publish_attempt_id::text,''),publish_fence,commit_command_id::text FROM agent.workspace_revision_commits WHERE tenant_id=$1 AND id=$2`, claim.TenantID, command.RevisionID).Scan(&result.RevisionID, &result.Status, &result.Version, &result.PublishedRevision, &result.PublishedHash, &result.ObservedRevision, &result.CompletedAt, &attemptID, &fence, &commitCommandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CompletedWorkspacePublish{}, false, nil
	}
	if err != nil {
		return CompletedWorkspacePublish{}, false, err
	}
	if result.Status != status {
		return CompletedWorkspacePublish{}, false, nil
	}
	if result.Version != command.ExpectedRevisionVersion+1 || attemptID != claim.AttemptID || fence != claim.Fence || commitCommandID != claim.CommandID || result.PublishedRevision != command.PublishedRevision || result.PublishedHash != command.PublishedHash || result.ObservedRevision != command.ObservedRevision {
		return CompletedWorkspacePublish{}, false, ErrWorkspaceConflict
	}
	var toolStatus, effectStatus, externalResourceRef string
	var toolVersion uint64
	var reconciliationDueAt *time.Time
	err = tx.QueryRow(ctx, `SELECT t.status,t.tool_call_version,e.status,COALESCE(e.external_resource_ref,''),e.reconciliation_due_at FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE t.tenant_id=$1 AND t.id=$2 AND e.id=$3`, claim.TenantID, claim.ToolCallID, claim.EffectID).Scan(&toolStatus, &toolVersion, &effectStatus, &externalResourceRef, &reconciliationDueAt)
	if err != nil {
		return CompletedWorkspacePublish{}, false, err
	}
	if toolStatus != string(command.Tool.TargetState) || toolVersion != command.Tool.ExpectedToolVersion+1 || effectStatus != status || externalResourceRef != effect.ExternalResourceRef {
		return CompletedWorkspacePublish{}, false, ErrWorkspaceConflict
	}
	if status == "outcome_unknown" {
		if reconciliationDueAt == nil || !reconciliationDueAt.Equal(effect.ReconciliationDueAt) {
			return CompletedWorkspacePublish{}, false, ErrWorkspaceConflict
		}
	} else if reconciliationDueAt != nil {
		return CompletedWorkspacePublish{}, false, ErrWorkspaceConflict
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type=$3 AND aggregate_kind='workspace_revision' AND aggregate_id=$4 AND aggregate_version=$5 AND store_epoch=$6)`, claim.TenantID, eventID, eventType, command.RevisionID, result.Version, store.StoreEpoch).Scan(&exists); err != nil {
		return CompletedWorkspacePublish{}, false, err
	}
	if !exists {
		return CompletedWorkspacePublish{}, false, ErrWorkspaceConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return CompletedWorkspacePublish{}, false, err
	}
	result.EventID, result.Replayed = eventID, true
	result.Tool = CompletedTool{ToolCallID: claim.ToolCallID, ToolCallVersion: toolVersion, Status: command.Tool.TargetState, CompletedAt: result.CompletedAt}
	return result, true, nil
}

func (store RunStore) workspacePublishIDs(stage, revisionID, attemptID string) (workspacePublishEventIDs, error) {
	values := make([]string, 3)
	for index, domain := range []string{"workspace-publish-" + stage + ":event", "workspace-publish-" + stage + ":outbox", "workspace-publish-" + stage + ":publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, revisionID+"\x00"+attemptID)
		if err != nil {
			return workspacePublishEventIDs{}, err
		}
		values[index] = value
	}
	return workspacePublishEventIDs{values[0], values[1], values[2]}, nil
}

func validBeginWorkspacePublish(command BeginWorkspacePublishCommand) bool {
	claim := command.Claim
	return command.RevisionID != "" && command.ExpectedRevisionVersion > 0 && validToolClaim(claim) && claim.EffectClass == "reconcilable_write" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.StartedEvent)
}

func validCompleteWorkspacePublish(command CompleteWorkspacePublishCommand, effect EffectCompletion) bool {
	if command.RevisionID == "" || command.ExpectedRevisionVersion < 1 || command.Tool.Claim.EffectClass != "reconcilable_write" || !validPointer(command.CompletionEvent) || !validEffectCompletion(command.Tool.TargetState, effect) {
		return false
	}
	switch command.Tool.TargetState {
	case statemachine.ToolCallSucceeded:
		return command.PublishedRevision != "" && command.PublishedHash != "" && command.ObservedRevision != ""
	case statemachine.ToolCallFailed:
		return command.PublishedRevision == "" && command.PublishedHash == ""
	case statemachine.ToolCallOutcomeUnknown:
		return command.PublishedRevision == "" && command.PublishedHash == ""
	default:
		return false
	}
}

func workspaceCompletionState(state statemachine.ToolCallState) (string, string) {
	switch state {
	case statemachine.ToolCallSucceeded:
		return "confirmed", "WorkspaceRevisionCommitted"
	case statemachine.ToolCallOutcomeUnknown:
		return "outcome_unknown", "WorkspaceRevisionCommitOutcomeUnknown"
	default:
		return "failed", "WorkspaceRevisionCommitRevoked"
	}
}
