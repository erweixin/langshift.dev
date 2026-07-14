package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type SweepExpiredWorkspacePublishCommand struct {
	RevisionID              string
	ExpectedRevisionVersion uint64
	Effect                  SweepExpiredToolEffectCommand
	ObservedRevision        string
	OutcomeUnknownEvent     PayloadPointer
	Actor                   json.RawMessage
	CorrelationID           string
}

type SweptWorkspacePublish struct {
	RevisionID, Status, EventID, ObservedRevision string
	Version                                       uint64
	OutcomeUnknownAt, ReconciliationDueAt         time.Time
	Effect                                        SweptToolEffect
}

func (store RunStore) SweepExpiredWorkspacePublish(ctx context.Context, command SweepExpiredWorkspacePublishCommand) (SweptWorkspacePublish, error) {
	if command.RevisionID == "" || command.ExpectedRevisionVersion < 1 || command.Effect.Candidate.EffectClass != "reconcilable_write" || !validPointer(command.OutcomeUnknownEvent) || !validJSONObject(command.Actor) || command.CorrelationID == "" {
		return SweptWorkspacePublish{}, ErrInvalidCommand
	}
	candidate := command.Effect.Candidate
	eventIDs, err := store.workspaceReconciliationIDs("lease-expired", command.RevisionID, candidate.EffectVersion+1)
	if err != nil {
		return SweptWorkspacePublish{}, err
	}
	var result SweptWorkspacePublish
	swept, err := store.sweepExpiredToolEffect(ctx, command.Effect, func(ctx context.Context, tx pgx.Tx, toolEventID string, leaseDigest []byte, now time.Time) error {
		nextVersion := command.ExpectedRevisionVersion + 1
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.workspace_revision_commits SET version=$1,status='outcome_unknown',observed_revision=NULLIF($2,''),publish_lease_hash=NULL,publish_lease_expires_at=NULL,outcome_unknown_at=$3,reconciliation_due_at=$4,updated_at=$3 WHERE tenant_id=$5 AND id=$6 AND status='publishing' AND version=$7 AND tool_call_id=$8 AND commit_command_id=$9 AND publish_attempt_id=$10 AND publish_fence=$11 AND publish_lease_hash=$12 AND publish_lease_expires_at=$13 AND publish_lease_expires_at<=$3`, nextVersion, command.ObservedRevision, now, command.Effect.Reconciliation.ReconciliationDueAt, candidate.TenantID, command.RevisionID, command.ExpectedRevisionVersion, candidate.ToolCallID, candidate.CommandID, candidate.AttemptID, candidate.Fence, leaseDigest, candidate.LeaseExpiresAt)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return ErrWorkspaceConflict
		}
		causationID := toolEventID
		event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: candidate.TenantID, UserID: candidate.UserID, EventType: "WorkspaceRevisionCommitOutcomeUnknown", SchemaVersion: 1, AggregateKind: "workspace_revision", AggregateID: command.RevisionID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.OutcomeUnknownEvent.Ref, PayloadHash: command.OutcomeUnknownEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.OutcomeUnknownEvent.Ref, PayloadHash: command.OutcomeUnknownEvent.Hash}}}
		if _, appendErr := store.Appender.Append(ctx, tx, event); appendErr != nil {
			return appendErr
		}
		result = SweptWorkspacePublish{RevisionID: command.RevisionID, Status: "outcome_unknown", EventID: eventIDs.event, ObservedRevision: command.ObservedRevision, Version: nextVersion, OutcomeUnknownAt: now, ReconciliationDueAt: command.Effect.Reconciliation.ReconciliationDueAt}
		return nil
	})
	if err != nil {
		return SweptWorkspacePublish{}, err
	}
	result.Effect = swept
	return result, nil
}
