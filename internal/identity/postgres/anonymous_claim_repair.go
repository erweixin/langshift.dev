package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/platform/ids"
)

var ErrAnonymousClaimRepairUnresolved = errors.New("anonymous claim manual review cannot be resolved from durable facts")

type AnonymousClaimRepairInspector struct {
	Pool        *pgxpool.Pool
	IdentityKey []byte
}

type AnonymousClaimRepairResolution struct {
	ClaimID, ClaimKey, TargetStatus, DestinationCommitEventID string
	TargetRouteRevisionID, EvidenceHash                       string
	ClaimVersion                                              uint64
	ReceiptCount                                              int
}

type anonymousClaimTargetFacts struct {
	ImportFound, FootprintFound                                           bool
	ImportID, ImportUserID, ImportMissionID, ImportSourceRevisionID       string
	ImportCommitEventID, MissionUserID, MissionClaimSetHash               string
	RouteID, RouteUserID, RouteMissionID, RouteClaimSetHash               string
	EventID, EventUserID, EventType, EventAggregateKind, EventAggregateID string
}

// InspectManualReview reads the destination by the immutable claim_key and
// returns the only state supported by durable facts. It never creates,
// deletes or changes a Mission, import, receipt or source claim.
func (inspector AnonymousClaimRepairInspector) InspectManualReview(ctx context.Context, saga anonymousclaim.Saga) (AnonymousClaimRepairResolution, error) {
	if inspector.Pool == nil || len(inspector.IdentityKey) < 32 || saga.ID == "" || saga.Status != anonymousclaim.ManualReview || saga.Version == 0 || saga.ClaimKey == "" || saga.TargetTenantID == "" || saga.TargetUserID == "" || saga.MissionID == "" || saga.SourceRouteRevisionID == "" || saga.ClaimSetHash == "" {
		return AnonymousClaimRepairResolution{}, anonymousclaim.ErrInvariant
	}
	targetRouteID, err := ids.DeterministicUUID(inspector.IdentityKey, "anonymous-claim-target-route", saga.ClaimKey)
	if err != nil {
		return AnonymousClaimRepairResolution{}, err
	}
	destinationEventID, err := ids.DeterministicUUID(inspector.IdentityKey, "anonymous-claim-destination-event", saga.ClaimKey)
	if err != nil {
		return AnonymousClaimRepairResolution{}, err
	}
	facts, err := inspector.loadTargetFacts(ctx, saga, targetRouteID, destinationEventID)
	if err != nil {
		return AnonymousClaimRepairResolution{}, err
	}
	return resolveAnonymousClaimManualReview(saga, targetRouteID, destinationEventID, facts)
}

func (inspector AnonymousClaimRepairInspector) loadTargetFacts(ctx context.Context, saga anonymousclaim.Saga, targetRouteID, destinationEventID string) (anonymousClaimTargetFacts, error) {
	tx, err := inspector.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return anonymousClaimTargetFacts{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, saga.TargetTenantID); err != nil {
		return anonymousClaimTargetFacts{}, err
	}
	var facts anonymousClaimTargetFacts
	err = tx.QueryRow(ctx, `SELECT id::text,user_id::text,mission_id::text,source_route_revision_id::text,commit_event_id::text FROM product.mission_imports WHERE tenant_id=$1 AND claim_key=$2`, saga.TargetTenantID, saga.ClaimKey).Scan(&facts.ImportID, &facts.ImportUserID, &facts.ImportMissionID, &facts.ImportSourceRevisionID, &facts.ImportCommitEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		var missionFound, routeFound, eventFound bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.missions WHERE tenant_id=$1 AND id=$2),EXISTS(SELECT 1 FROM product.route_revisions WHERE tenant_id=$1 AND id=$3),EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$4)`, saga.TargetTenantID, saga.MissionID, targetRouteID, destinationEventID).Scan(&missionFound, &routeFound, &eventFound)
		facts.FootprintFound = missionFound || routeFound || eventFound
		if err != nil {
			return anonymousClaimTargetFacts{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return anonymousClaimTargetFacts{}, err
		}
		return facts, nil
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
	if err = tx.Commit(ctx); err != nil {
		return anonymousClaimTargetFacts{}, err
	}
	return facts, nil
}

func resolveAnonymousClaimManualReview(saga anonymousclaim.Saga, targetRouteID, destinationEventID string, facts anonymousClaimTargetFacts) (AnonymousClaimRepairResolution, error) {
	if err := validateAnonymousClaimRepairReceipts(saga); err != nil {
		return AnonymousClaimRepairResolution{}, ErrAnonymousClaimRepairUnresolved
	}
	targetStatus := anonymousclaim.Reserved
	resolvedEventID := ""
	if !facts.ImportFound {
		if facts.FootprintFound || saga.DestinationCommitEventID != "" || len(saga.DeletionReceipts) != 0 {
			return AnonymousClaimRepairResolution{}, ErrAnonymousClaimRepairUnresolved
		}
	} else {
		exact := facts.ImportUserID == saga.TargetUserID && facts.ImportMissionID == saga.MissionID && facts.ImportSourceRevisionID == saga.SourceRouteRevisionID && facts.ImportCommitEventID == destinationEventID &&
			facts.MissionUserID == saga.TargetUserID && facts.MissionClaimSetHash == saga.ClaimSetHash && facts.RouteID == targetRouteID && facts.RouteUserID == saga.TargetUserID && facts.RouteMissionID == saga.MissionID && facts.RouteClaimSetHash == saga.ClaimSetHash &&
			facts.EventID == destinationEventID && facts.EventUserID == saga.TargetUserID && facts.EventType == "AnonymousClaimDestinationCommitted" && facts.EventAggregateKind == "anonymous_claim" && facts.EventAggregateID == saga.ID &&
			(saga.DestinationCommitEventID == "" || saga.DestinationCommitEventID == destinationEventID)
		if !exact {
			return AnonymousClaimRepairResolution{}, ErrAnonymousClaimRepairUnresolved
		}
		resolvedEventID = destinationEventID
		targetStatus = anonymousclaim.DestinationCommitted
		if len(saga.DeletionReceipts) != 0 {
			targetStatus = anonymousclaim.Erasing
		}
	}
	successor, err := anonymousclaim.Advance(saga, anonymousclaim.Input{ExpectedVersion: saga.Version, Command: anonymousclaim.Reconcile, ReconciledStatus: targetStatus, DestinationCommitEventID: resolvedEventID})
	if err != nil {
		return AnonymousClaimRepairResolution{}, ErrAnonymousClaimRepairUnresolved
	}
	evidence, _ := json.Marshal(struct {
		ClaimID, ClaimKey, ClaimSetHash, TargetTenantID, TargetUserID, MissionID string
		SourceRouteRevisionID, TargetRouteRevisionID, DestinationEventID         string
		PreviousVersion, ResolvedVersion                                         uint64
		ResolvedStatus                                                           anonymousclaim.Status
		ReceiptCount                                                             int
	}{saga.ID, saga.ClaimKey, saga.ClaimSetHash, saga.TargetTenantID, saga.TargetUserID, saga.MissionID, saga.SourceRouteRevisionID, targetRouteID, resolvedEventID, saga.Version, successor.Version, successor.Status, len(saga.DeletionReceipts)})
	digest := sha256.Sum256(evidence)
	return AnonymousClaimRepairResolution{ClaimID: saga.ID, ClaimKey: saga.ClaimKey, TargetStatus: string(successor.Status), DestinationCommitEventID: successor.DestinationCommitEventID, TargetRouteRevisionID: targetRouteID, EvidenceHash: hex.EncodeToString(digest[:]), ClaimVersion: successor.Version, ReceiptCount: len(saga.DeletionReceipts)}, nil
}

func validateAnonymousClaimRepairReceipts(saga anonymousclaim.Saga) error {
	probe := anonymousclaim.Saga{Status: anonymousclaim.Erasing, Version: 1}
	for _, receipt := range saga.DeletionReceipts {
		next, err := anonymousclaim.Advance(probe, anonymousclaim.Input{ExpectedVersion: probe.Version, Command: anonymousclaim.RecordDeletionReceipt, DeletionReceipt: receipt})
		if err != nil || next.Version == probe.Version {
			return anonymousclaim.ErrInvariant
		}
		probe = next
	}
	return nil
}
