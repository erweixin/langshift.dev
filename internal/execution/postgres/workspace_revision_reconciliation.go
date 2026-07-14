package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

type ClaimWorkspaceReconciliationCommand struct {
	RevisionID              string
	ExpectedRevisionVersion uint64
	Reconciliation          ClaimReconciliationCommand
}

type WorkspaceReconciliationClaim struct {
	RevisionID string
	Version    uint64
	Claim      ReconciliationClaim
}

type DeferWorkspaceReconciliationCommand struct {
	Workspace        WorkspaceReconciliationClaim
	Reconciliation   DeferReconciliationCommand
	ObservedRevision string
	DeferredEvent    PayloadPointer
	Actor            json.RawMessage
	CorrelationID    string
}

type DeferredWorkspaceReconciliation struct {
	RevisionID, Status, EventID, ObservedRevision string
	Version                                       uint64
	DueAt                                         time.Time
	Reconciliation                                DeferredReconciliation
}

type CompleteWorkspaceReconciliationCommand struct {
	Workspace         WorkspaceReconciliationClaim
	Reconciliation    CompleteReconciliationCommand
	PublishedRevision string
	PublishedHash     string
	ObservedRevision  string
	CompletionEvent   PayloadPointer
}

type CompletedWorkspaceReconciliation struct {
	RevisionID, Status, EventID, PublishedRevision, PublishedHash, ObservedRevision string
	Version                                                                         uint64
	CompletedAt                                                                     time.Time
	Tool                                                                            CompletedTool
}

func (store RunStore) ClaimWorkspaceReconciliation(ctx context.Context, command ClaimWorkspaceReconciliationCommand) (WorkspaceReconciliationClaim, error) {
	if command.RevisionID == "" || command.ExpectedRevisionVersion < 1 {
		return WorkspaceReconciliationClaim{}, ErrInvalidCommand
	}
	var workspace WorkspaceReconciliationClaim
	claim, err := store.claimReconciliation(ctx, command.Reconciliation, func(ctx context.Context, tx pgx.Tx, claim ReconciliationClaim, now time.Time) error {
		var status, toolCallID, effectKey string
		var version uint64
		var dueAt time.Time
		queryErr := tx.QueryRow(ctx, `SELECT status,version,tool_call_id::text,effect_key,reconciliation_due_at FROM agent.workspace_revision_commits WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, claim.TenantID, command.RevisionID).Scan(&status, &version, &toolCallID, &effectKey, &dueAt)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return ErrWorkspaceNotActionable
		}
		if queryErr != nil {
			return queryErr
		}
		if status != "outcome_unknown" || version != command.ExpectedRevisionVersion || toolCallID != claim.ToolCallID || effectKey != claim.EffectKey || !dueAt.Equal(claim.ReconciliationDueAt) || dueAt.After(now) {
			return ErrWorkspaceNotActionable
		}
		workspace = WorkspaceReconciliationClaim{RevisionID: command.RevisionID, Version: version, Claim: claim}
		return nil
	})
	if err != nil {
		return WorkspaceReconciliationClaim{}, err
	}
	workspace.Claim = claim
	return workspace, nil
}

func (store RunStore) DeferWorkspaceReconciliation(ctx context.Context, command DeferWorkspaceReconciliationCommand) (DeferredWorkspaceReconciliation, error) {
	if !validDeferWorkspaceReconciliation(command) {
		return DeferredWorkspaceReconciliation{}, ErrInvalidCommand
	}
	workspace := command.Workspace
	eventIDs, err := store.workspaceReconciliationIDs("deferred", workspace.RevisionID, command.Reconciliation.Claim.EffectVersion+1)
	if err != nil {
		return DeferredWorkspaceReconciliation{}, err
	}
	var result DeferredWorkspaceReconciliation
	deferred, err := store.deferReconciliation(ctx, command.Reconciliation, func(ctx context.Context, tx pgx.Tx, attemptEventID string, now time.Time) error {
		claim := command.Reconciliation.Claim
		nextVersion := workspace.Version + 1
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.workspace_revision_commits SET version=$1,observed_revision=COALESCE(NULLIF($2,''),observed_revision),reconciliation_due_at=$3,reconciliation_attempts=reconciliation_attempts+1,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND status='outcome_unknown' AND version=$7 AND tool_call_id=$8 AND effect_key=$9 AND reconciliation_due_at=$10`, nextVersion, command.ObservedRevision, command.Reconciliation.NextDueAt, now, claim.TenantID, workspace.RevisionID, workspace.Version, claim.ToolCallID, claim.EffectKey, claim.ReconciliationDueAt)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return ErrWorkspaceConflict
		}
		causationID := attemptEventID
		event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: claim.TenantID, UserID: claim.UserID, EventType: "WorkspaceRevisionCommitOutcomeUnknown", SchemaVersion: 1, AggregateKind: "workspace_revision", AggregateID: workspace.RevisionID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.DeferredEvent.Ref, PayloadHash: command.DeferredEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.DeferredEvent.Ref, PayloadHash: command.DeferredEvent.Hash}}}
		if _, appendErr := store.Appender.Append(ctx, tx, event); appendErr != nil {
			return appendErr
		}
		result = DeferredWorkspaceReconciliation{RevisionID: workspace.RevisionID, Status: "outcome_unknown", EventID: eventIDs.event, ObservedRevision: command.ObservedRevision, Version: nextVersion, DueAt: command.Reconciliation.NextDueAt}
		return nil
	})
	if err != nil {
		return DeferredWorkspaceReconciliation{}, err
	}
	result.Reconciliation = deferred
	return result, nil
}

func (store RunStore) CompleteWorkspaceReconciliation(ctx context.Context, command CompleteWorkspaceReconciliationCommand) (CompletedWorkspaceReconciliation, error) {
	if !validCompleteWorkspaceReconciliation(command) {
		return CompletedWorkspaceReconciliation{}, ErrInvalidCommand
	}
	workspace := command.Workspace
	status, eventType := workspaceCompletionState(command.Reconciliation.TargetState)
	eventIDs, err := store.workspaceReconciliationIDs(status, workspace.RevisionID, command.Reconciliation.Claim.EffectVersion+1)
	if err != nil {
		return CompletedWorkspaceReconciliation{}, err
	}
	var result CompletedWorkspaceReconciliation
	tool, err := store.completeReconciliation(ctx, command.Reconciliation, func(ctx context.Context, tx pgx.Tx, toolEventID string, now time.Time) error {
		claim := command.Reconciliation.Claim
		nextVersion := workspace.Version + 1
		var publishedRevision, publishedHash any
		var confirmedAt, failedAt any
		if status == "confirmed" {
			publishedRevision, publishedHash, confirmedAt = command.PublishedRevision, command.PublishedHash, now
		} else {
			failedAt = now
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.workspace_revision_commits SET version=$1,status=$2,published_revision=$3,published_hash=$4,observed_revision=NULLIF($5,''),outcome_unknown_at=NULL,reconciliation_due_at=NULL,reconciliation_attempts=reconciliation_attempts+1,confirmed_at=$6,failed_at=$7,updated_at=$8 WHERE tenant_id=$9 AND id=$10 AND status='outcome_unknown' AND version=$11 AND tool_call_id=$12 AND effect_key=$13 AND reconciliation_due_at=$14`, nextVersion, status, publishedRevision, publishedHash, command.ObservedRevision, confirmedAt, failedAt, now, claim.TenantID, workspace.RevisionID, workspace.Version, claim.ToolCallID, claim.EffectKey, claim.ReconciliationDueAt)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return ErrWorkspaceConflict
		}
		causationID := toolEventID
		event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: claim.TenantID, UserID: claim.UserID, EventType: eventType, SchemaVersion: 1, AggregateKind: "workspace_revision", AggregateID: workspace.RevisionID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Reconciliation.Actor, CausationID: &causationID, CorrelationID: command.Reconciliation.CorrelationID, PayloadRef: command.CompletionEvent.Ref, PayloadHash: command.CompletionEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.CompletionEvent.Ref, PayloadHash: command.CompletionEvent.Hash}}}
		if _, appendErr := store.Appender.Append(ctx, tx, event); appendErr != nil {
			return appendErr
		}
		result = CompletedWorkspaceReconciliation{RevisionID: workspace.RevisionID, Status: status, EventID: eventIDs.event, PublishedRevision: command.PublishedRevision, PublishedHash: command.PublishedHash, ObservedRevision: command.ObservedRevision, Version: nextVersion, CompletedAt: now}
		return nil
	})
	if err != nil {
		return CompletedWorkspaceReconciliation{}, err
	}
	result.Tool = tool
	return result, nil
}

type workspaceReconciliationEventIDs struct{ event, outbox, publish string }

func (store RunStore) workspaceReconciliationIDs(stage, revisionID string, effectVersion uint64) (workspaceReconciliationEventIDs, error) {
	values := make([]string, 3)
	scope := revisionID + "\x00" + stage + "\x00" + strconv.FormatUint(effectVersion, 10)
	for index, domain := range []string{"workspace-reconciliation:event", "workspace-reconciliation:outbox", "workspace-reconciliation:publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, scope)
		if err != nil {
			return workspaceReconciliationEventIDs{}, err
		}
		values[index] = value
	}
	return workspaceReconciliationEventIDs{values[0], values[1], values[2]}, nil
}

func validDeferWorkspaceReconciliation(command DeferWorkspaceReconciliationCommand) bool {
	workspace := command.Workspace
	claim := command.Reconciliation.Claim
	return workspace.RevisionID != "" && workspace.Version > 0 && workspace.Claim.TenantID == claim.TenantID && workspace.Claim.StoreEpoch == claim.StoreEpoch && workspace.Claim.ToolCallID == claim.ToolCallID && workspace.Claim.EffectID == claim.EffectID && workspace.Claim.AttemptID == claim.AttemptID && validPointer(command.DeferredEvent) && validJSONObject(command.Actor) && command.CorrelationID != ""
}

func validCompleteWorkspaceReconciliation(command CompleteWorkspaceReconciliationCommand) bool {
	workspace := command.Workspace
	claim := command.Reconciliation.Claim
	if workspace.RevisionID == "" || workspace.Version < 1 || workspace.Claim.TenantID != claim.TenantID || workspace.Claim.StoreEpoch != claim.StoreEpoch || workspace.Claim.ToolCallID != claim.ToolCallID || workspace.Claim.EffectID != claim.EffectID || workspace.Claim.AttemptID != claim.AttemptID || !validPointer(command.CompletionEvent) {
		return false
	}
	switch command.Reconciliation.TargetState {
	case statemachine.ToolCallSucceeded:
		return command.PublishedRevision != "" && command.PublishedHash != "" && command.ObservedRevision != ""
	case statemachine.ToolCallFailed:
		return command.PublishedRevision == "" && command.PublishedHash == ""
	default:
		return false
	}
}
