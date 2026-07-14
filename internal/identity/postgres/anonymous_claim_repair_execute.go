package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/platform/ids"
)

func (service AnonymousClaimRepairControlService) executeAnonymousClaimRepair(ctx context.Context, targetTenantID, repairID, correlationID string, actor json.RawMessage) (claimRepairSnapshot, error) {
	if err := service.requireCurrentEpoch(ctx); err != nil {
		return claimRepairSnapshot{}, err
	}
	snapshot, err := service.loadClaimRepairSnapshot(ctx, targetTenantID, repairID)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	if snapshot.Status == "executed" {
		return snapshot, nil
	}
	if snapshot.Status != "approved" || !snapshot.ExpiresAt.After(service.now()) {
		return claimRepairSnapshot{}, errClaimRepairNotActionable
	}
	saga, err := service.ClaimStore.Load(ctx, snapshot.ClaimID)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	resolution, err := service.Inspector.InspectManualReview(ctx, saga)
	if err != nil || saga.TargetTenantID != targetTenantID || saga.Version != snapshot.ClaimVersion || saga.ClaimKey != snapshot.ClaimKey || resolution.EvidenceHash != snapshot.EvidenceHash || claimRepairResolutionForStatus(resolution.TargetStatus) != snapshot.Resolution {
		return claimRepairSnapshot{}, errClaimRepairNotActionable
	}
	next, err := anonymousclaim.Advance(saga, anonymousclaim.Input{ExpectedVersion: saga.Version, Command: anonymousclaim.Reconcile, ReconciledStatus: anonymousclaim.Status(resolution.TargetStatus), DestinationCommitEventID: resolution.DestinationCommitEventID})
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	resolvedIDs, err := service.claimRepairIDs("resolved", repairID)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	reconcileOutboxID, err := ids.DeterministicUUID(service.IDKey, "anonymous-claim-repair:resolved:reconcile-outbox", repairID)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	reconcileCommandID, err := ids.DeterministicUUID(service.IDKey, "anonymous-claim-repair:resolved:reconcile-command", repairID)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	executedIDs, err := service.claimRepairIDs("executed", repairID)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	resolvedPayload, err := service.putClaimRepairJSON(ctx, service.SystemTenantID, resolvedIDs.event, claimRepairEventClass, map[string]any{
		"subject_id": saga.ID, "subject_version": next.Version, "payload_ref": snapshot.EvidenceRef, "claim_set_hash": saga.ClaimSetHash,
		"claim_id": saga.ID, "claim_key": saga.ClaimKey, "repair_command_id": repairID, "previous_claim_version": saga.Version,
		"restored_status": resolution.TargetStatus, "destination_commit_event_id": nullableClaimRepairValue(resolution.DestinationCommitEventID), "evidence_hash": resolution.EvidenceHash,
	})
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	reconcilePayload, err := service.putClaimRepairJSON(ctx, service.SystemTenantID, reconcileCommandID, claimRepairCommandClass, map[string]any{"claim_id": saga.ID})
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	executedPayload, err := service.putClaimRepairJSON(ctx, targetTenantID, executedIDs.event, claimRepairEventClass, map[string]any{
		"subject_id": repairID, "subject_version": snapshot.Version + 1, "command_id": nil, "proposal_hash": snapshot.ProposalHash,
		"repair_command_id": repairID, "target_kind": "anonymous_claim", "target_id": saga.ID, "previous_target_version": saga.Version,
		"result_event_ids": []string{resolvedIDs.event},
	})
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	return service.executeAnonymousClaimRepairTransaction(ctx, executeAnonymousClaimRepairCommand{
		Snapshot: snapshot, Saga: saga, Next: next, Resolution: resolution, TargetTenantID: targetTenantID,
		CorrelationID: correlationID, Actor: actor, ResolvedEvent: resolvedPayload, ReconcileCommand: reconcilePayload,
		ResolvedIDs: resolvedIDs, ReconcileOutboxID: reconcileOutboxID, ReconcileCommandID: reconcileCommandID,
		ExecutedEvent: executedPayload, ExecutedIDs: executedIDs,
	})
}

type executeAnonymousClaimRepairCommand struct {
	Snapshot                                       claimRepairSnapshot
	Saga, Next                                     anonymousclaim.Saga
	Resolution                                     AnonymousClaimRepairResolution
	TargetTenantID, CorrelationID                  string
	Actor                                          json.RawMessage
	ResolvedEvent, ReconcileCommand, ExecutedEvent claimRepairPointer
	ResolvedIDs, ExecutedIDs                       claimRepairEventIDs
	ReconcileOutboxID, ReconcileCommandID          string
}

func (service AnonymousClaimRepairControlService) executeAnonymousClaimRepairTransaction(ctx context.Context, command executeAnonymousClaimRepairCommand) (claimRepairSnapshot, error) {
	now := service.now()
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TargetTenantID); err != nil {
		return claimRepairSnapshot{}, err
	}
	var actual claimRepairSnapshot
	err = tx.QueryRow(ctx, `SELECT id::text,status,version,resolution,proposal_hash,evidence_hash,evidence_payload_ref,source_tenant_id::text,anonymous_claim_id::text,effect_key,initiator_user_id::text,target_version,updated_at,expires_at FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2 AND repair_kind='anonymous_claim_reconciliation' FOR UPDATE`, command.TargetTenantID, command.Snapshot.ID).Scan(&actual.ID, &actual.Status, &actual.Version, &actual.Resolution, &actual.ProposalHash, &actual.EvidenceHash, &actual.EvidenceRef, &actual.SourceTenantID, &actual.ClaimID, &actual.ClaimKey, &actual.InitiatorID, &actual.ClaimVersion, &actual.UpdatedAt, &actual.ExpiresAt)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	if actual.Status == "executed" {
		var eventExists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='RepairCommandExecuted' AND aggregate_kind='repair_command' AND aggregate_id=$3 AND aggregate_version=$4)`, command.TargetTenantID, command.ExecutedIDs.event, actual.ID, actual.Version).Scan(&eventExists); err != nil || !eventExists {
			return claimRepairSnapshot{}, errClaimRepairConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return claimRepairSnapshot{}, err
		}
		return actual, nil
	}
	if actual.Status != "approved" || actual.Version != command.Snapshot.Version || actual.ProposalHash != command.Snapshot.ProposalHash || actual.EvidenceHash != command.Snapshot.EvidenceHash || actual.EvidenceRef != command.Snapshot.EvidenceRef || actual.SourceTenantID != service.SystemTenantID || actual.ClaimID != command.Saga.ID || actual.ClaimKey != command.Saga.ClaimKey || actual.ClaimVersion != command.Saga.Version || actual.Resolution != command.Snapshot.Resolution || !actual.ExpiresAt.After(now) {
		return claimRepairSnapshot{}, errClaimRepairNotActionable
	}
	var approvals uint64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND decision='approve' AND proposal_hash=$3 AND target_version=$4 AND effect_version=$4`, command.TargetTenantID, actual.ID, actual.ProposalHash, actual.ClaimVersion).Scan(&approvals); err != nil || approvals != 2 {
		return claimRepairSnapshot{}, errClaimRepairNotActionable
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, service.SystemTenantID); err != nil {
		return claimRepairSnapshot{}, err
	}
	locked, err := loadAnonymousClaimRepairSaga(ctx, tx, service.SystemTenantID, command.Saga.ID, true)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	if locked.Status != anonymousclaim.ManualReview || locked.Version != command.Saga.Version || locked.ClaimKey != command.Saga.ClaimKey || locked.TargetTenantID != command.TargetTenantID {
		return claimRepairSnapshot{}, errClaimRepairNotActionable
	}
	targetRouteID, err := ids.DeterministicUUID(service.Inspector.IdentityKey, "anonymous-claim-target-route", locked.ClaimKey)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	destinationEventID, err := ids.DeterministicUUID(service.Inspector.IdentityKey, "anonymous-claim-destination-event", locked.ClaimKey)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	facts, err := loadAnonymousClaimTargetFactsTx(ctx, tx, locked, targetRouteID, destinationEventID)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	verified, err := resolveAnonymousClaimManualReview(locked, targetRouteID, destinationEventID, facts)
	if err != nil || verified.EvidenceHash != actual.EvidenceHash || claimRepairResolutionForStatus(verified.TargetStatus) != actual.Resolution {
		return claimRepairSnapshot{}, errClaimRepairNotActionable
	}
	next, err := anonymousclaim.Advance(locked, anonymousclaim.Input{ExpectedVersion: locked.Version, Command: anonymousclaim.Reconcile, ReconciledStatus: anonymousclaim.Status(verified.TargetStatus), DestinationCommitEventID: verified.DestinationCommitEventID})
	if err != nil || next.Version != command.Next.Version || next.Status != command.Next.Status || next.DestinationCommitEventID != command.Next.DestinationCommitEventID {
		return claimRepairSnapshot{}, errClaimRepairNotActionable
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, service.SystemTenantID); err != nil {
		return claimRepairSnapshot{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE identity.onboarding_claims SET version=$1,status=$2,destination_commit_event_id=NULLIF($3,'')::uuid,destination_committed_at=CASE WHEN $2 IN ('destination_committed','erasing') THEN COALESCE(destination_committed_at,$4) ELSE destination_committed_at END,erasing_at=CASE WHEN $2='erasing' THEN COALESCE(erasing_at,$4) ELSE erasing_at END,updated_at=$4 WHERE tenant_id=$5 AND id=$6 AND version=$7 AND status='manual_review' AND claim_key=$8`, next.Version, next.Status, next.DestinationCommitEventID, now, service.SystemTenantID, locked.ID, locked.Version, locked.ClaimKey)
	if err != nil || tag.RowsAffected() != 1 {
		return claimRepairSnapshot{}, anonymousclaim.ErrVersionConflict
	}
	approvedID, err := service.claimRepairIDs("approved", actual.ID)
	if err != nil {
		return claimRepairSnapshot{}, err
	}
	resolved := eventpostgres.Input{Event: eventpostgres.Event{ID: command.ResolvedIDs.event, TenantID: service.SystemTenantID, UserID: locked.EphemeralUserID, EventType: "AnonymousClaimManualReviewResolved", SchemaVersion: 1, AggregateKind: "anonymous_claim", AggregateID: locked.ID, AggregateVersion: next.Version, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &approvedID.event, CorrelationID: command.CorrelationID, PayloadRef: command.ResolvedEvent.Ref, PayloadHash: command.ResolvedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{
		{ID: command.ResolvedIDs.outbox, CommandID: command.ResolvedIDs.publish, CommandType: "events.publish", PayloadRef: command.ResolvedEvent.Ref, PayloadHash: command.ResolvedEvent.Hash},
		{ID: command.ReconcileOutboxID, CommandID: command.ReconcileCommandID, CommandType: AnonymousClaimReconcileCommand, TargetAggregateKind: "anonymous_claim", TargetAggregateID: locked.ID, PayloadRef: command.ReconcileCommand.Ref, PayloadHash: command.ReconcileCommand.Hash},
	}}
	if _, err = service.Appender.Append(ctx, tx, resolved); err != nil {
		return claimRepairSnapshot{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TargetTenantID); err != nil {
		return claimRepairSnapshot{}, err
	}
	tag, err = tx.Exec(ctx, `UPDATE agent.repair_commands SET version=version+1,status='executed',executed_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND status='approved' AND expires_at>$1`, now, command.TargetTenantID, actual.ID, actual.Version)
	if err != nil || tag.RowsAffected() != 1 {
		return claimRepairSnapshot{}, errClaimRepairConflict
	}
	executed := eventpostgres.Input{Event: eventpostgres.Event{ID: command.ExecutedIDs.event, TenantID: command.TargetTenantID, UserID: locked.TargetUserID, EventType: "RepairCommandExecuted", SchemaVersion: 1, AggregateKind: "repair_command", AggregateID: actual.ID, AggregateVersion: actual.Version + 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &command.ResolvedIDs.event, CorrelationID: command.CorrelationID, PayloadRef: command.ExecutedEvent.Ref, PayloadHash: command.ExecutedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: command.ExecutedIDs.outbox, CommandID: command.ExecutedIDs.publish, CommandType: "events.publish", PayloadRef: command.ExecutedEvent.Ref, PayloadHash: command.ExecutedEvent.Hash}}}
	if _, err = service.Appender.Append(ctx, tx, executed); err != nil {
		return claimRepairSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return claimRepairSnapshot{}, err
	}
	actual.Status, actual.Version, actual.UpdatedAt = "executed", actual.Version+1, now
	return actual, nil
}

func loadAnonymousClaimRepairSaga(ctx context.Context, tx pgx.Tx, systemTenantID, claimID string, lock bool) (anonymousclaim.Saga, error) {
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE OF c"
	}
	var saga anonymousclaim.Saga
	var reservedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT c.id::text,c.anonymous_subject_id::text,c.user_id::text,c.onboarding_session_id::text,c.source_route_revision_id::text,COALESCE(r.claim_set_hash,''),c.status,c.version,c.claim_key,COALESCE(c.target_tenant_id::text,''),COALESCE(c.target_user_id::text,''),COALESCE(c.target_mission_id::text,''),COALESCE(c.destination_commit_event_id::text,''),c.reserved_at,c.expires_at FROM identity.onboarding_claims c LEFT JOIN product.route_revisions r ON r.id=c.source_route_revision_id AND r.tenant_id=c.tenant_id WHERE c.id=$1 AND c.tenant_id=$2`+lockClause, claimID, systemTenantID).Scan(&saga.ID, &saga.AnonymousSubjectID, &saga.EphemeralUserID, &saga.OnboardingSessionID, &saga.SourceRouteRevisionID, &saga.ClaimSetHash, &saga.Status, &saga.Version, &saga.ClaimKey, &saga.TargetTenantID, &saga.TargetUserID, &saga.MissionID, &saga.DestinationCommitEventID, &reservedAt, &saga.ExpiresAt)
	if err != nil {
		return anonymousclaim.Saga{}, err
	}
	if reservedAt != nil {
		saga.ReservedAt = reservedAt.UTC()
	}
	rows, err := tx.Query(ctx, `SELECT id::text,surface,receipt_hash,erased_at,details FROM identity.anonymous_erasure_receipts WHERE tenant_id=$1 AND claim_id=$2 ORDER BY surface`, systemTenantID, claimID)
	if err != nil {
		return anonymousclaim.Saga{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var receipt anonymousclaim.DeletionReceipt
		if err = rows.Scan(&receipt.ID, &receipt.Surface, &receipt.Hash, &receipt.ErasedAt, &receipt.Details); err != nil {
			return anonymousclaim.Saga{}, err
		}
		saga.DeletionReceipts = append(saga.DeletionReceipts, receipt)
	}
	return saga, rows.Err()
}

func loadAnonymousClaimTargetFactsTx(ctx context.Context, tx pgx.Tx, saga anonymousclaim.Saga, targetRouteID, destinationEventID string) (anonymousClaimTargetFacts, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, saga.TargetTenantID); err != nil {
		return anonymousClaimTargetFacts{}, err
	}
	var facts anonymousClaimTargetFacts
	err := tx.QueryRow(ctx, `SELECT id::text,user_id::text,mission_id::text,source_route_revision_id::text,commit_event_id::text FROM product.mission_imports WHERE tenant_id=$1 AND claim_key=$2`, saga.TargetTenantID, saga.ClaimKey).Scan(&facts.ImportID, &facts.ImportUserID, &facts.ImportMissionID, &facts.ImportSourceRevisionID, &facts.ImportCommitEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		var missionFound, routeFound, eventFound bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.missions WHERE tenant_id=$1 AND id=$2),EXISTS(SELECT 1 FROM product.route_revisions WHERE tenant_id=$1 AND id=$3),EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$4)`, saga.TargetTenantID, saga.MissionID, targetRouteID, destinationEventID).Scan(&missionFound, &routeFound, &eventFound)
		facts.FootprintFound = missionFound || routeFound || eventFound
		return facts, err
	}
	if err != nil {
		return anonymousClaimTargetFacts{}, err
	}
	facts.ImportFound = true
	if err = tx.QueryRow(ctx, `SELECT user_id::text,claim_set_hash FROM product.missions WHERE tenant_id=$1 AND id=$2`, saga.TargetTenantID, saga.MissionID).Scan(&facts.MissionUserID, &facts.MissionClaimSetHash); err != nil {
		return anonymousClaimTargetFacts{}, ErrAnonymousClaimRepairUnresolved
	}
	if err = tx.QueryRow(ctx, `SELECT id::text,user_id::text,mission_id::text,claim_set_hash FROM product.route_revisions WHERE tenant_id=$1 AND id=$2`, saga.TargetTenantID, targetRouteID).Scan(&facts.RouteID, &facts.RouteUserID, &facts.RouteMissionID, &facts.RouteClaimSetHash); err != nil {
		return anonymousClaimTargetFacts{}, ErrAnonymousClaimRepairUnresolved
	}
	if err = tx.QueryRow(ctx, `SELECT id::text,user_id::text,event_type,aggregate_kind,aggregate_id::text FROM agent.events WHERE tenant_id=$1 AND id=$2`, saga.TargetTenantID, destinationEventID).Scan(&facts.EventID, &facts.EventUserID, &facts.EventType, &facts.EventAggregateKind, &facts.EventAggregateID); err != nil {
		return anonymousClaimTargetFacts{}, ErrAnonymousClaimRepairUnresolved
	}
	return facts, nil
}

func nullableClaimRepairValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}
