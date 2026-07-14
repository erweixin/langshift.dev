package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/anonymousclaim"
)

func TestResolveAnonymousClaimManualReviewUsesOnlyExactDurableFacts(t *testing.T) {
	saga := anonymousclaim.Saga{ID: "claim", AnonymousSubjectID: "subject", Status: anonymousclaim.ManualReview, Version: 3, ClaimKey: "claim-key", TargetTenantID: "tenant", TargetUserID: "user", MissionID: "mission", SourceRouteRevisionID: "source-route", ClaimSetHash: "claim-set"}
	targetRouteID, destinationEventID := "target-route", "destination-event"
	reserved, err := resolveAnonymousClaimManualReview(saga, targetRouteID, destinationEventID, anonymousClaimTargetFacts{})
	if err != nil || reserved.TargetStatus != string(anonymousclaim.Reserved) || reserved.DestinationCommitEventID != "" || reserved.ClaimVersion != 4 || reserved.EvidenceHash == "" {
		t.Fatalf("reserved=%#v err=%v", reserved, err)
	}
	exact := exactAnonymousClaimTargetFacts(saga, targetRouteID, destinationEventID)
	committed, err := resolveAnonymousClaimManualReview(saga, targetRouteID, destinationEventID, exact)
	if err != nil || committed.TargetStatus != string(anonymousclaim.DestinationCommitted) || committed.DestinationCommitEventID != destinationEventID || committed.ClaimVersion != 4 {
		t.Fatalf("committed=%#v err=%v", committed, err)
	}
	saga.DestinationCommitEventID = destinationEventID
	saga.DeletionReceipts = []anonymousclaim.DeletionReceipt{{ID: "receipt", Surface: "body_payload", Hash: "receipt-hash", ErasedAt: time.Unix(1_800_000_000, 0).UTC(), Details: json.RawMessage(`{"verified":true}`)}}
	erasing, err := resolveAnonymousClaimManualReview(saga, targetRouteID, destinationEventID, exact)
	if err != nil || erasing.TargetStatus != string(anonymousclaim.Erasing) || erasing.ReceiptCount != 1 {
		t.Fatalf("erasing=%#v err=%v", erasing, err)
	}
}

func TestResolveAnonymousClaimManualReviewRejectsPartialOrMismatchedTarget(t *testing.T) {
	saga := anonymousclaim.Saga{ID: "claim", Status: anonymousclaim.ManualReview, Version: 3, ClaimKey: "claim-key", TargetTenantID: "tenant", TargetUserID: "user", MissionID: "mission", SourceRouteRevisionID: "source-route", ClaimSetHash: "claim-set"}
	if _, err := resolveAnonymousClaimManualReview(saga, "route", "event", anonymousClaimTargetFacts{FootprintFound: true}); !errors.Is(err, ErrAnonymousClaimRepairUnresolved) {
		t.Fatalf("partial target error=%v", err)
	}
	facts := exactAnonymousClaimTargetFacts(saga, "route", "event")
	facts.RouteClaimSetHash = "different"
	if _, err := resolveAnonymousClaimManualReview(saga, "route", "event", facts); !errors.Is(err, ErrAnonymousClaimRepairUnresolved) {
		t.Fatalf("hash mismatch error=%v", err)
	}
	facts = exactAnonymousClaimTargetFacts(saga, "route", "event")
	facts.EventAggregateID = "other-claim"
	if _, err := resolveAnonymousClaimManualReview(saga, "route", "event", facts); !errors.Is(err, ErrAnonymousClaimRepairUnresolved) {
		t.Fatalf("event mismatch error=%v", err)
	}
}

func exactAnonymousClaimTargetFacts(saga anonymousclaim.Saga, targetRouteID, destinationEventID string) anonymousClaimTargetFacts {
	return anonymousClaimTargetFacts{
		ImportFound: true, ImportID: "import", ImportUserID: saga.TargetUserID, ImportMissionID: saga.MissionID,
		ImportSourceRevisionID: saga.SourceRouteRevisionID, ImportCommitEventID: destinationEventID,
		MissionUserID: saga.TargetUserID, MissionClaimSetHash: saga.ClaimSetHash,
		RouteID: targetRouteID, RouteUserID: saga.TargetUserID, RouteMissionID: saga.MissionID, RouteClaimSetHash: saga.ClaimSetHash,
		EventID: destinationEventID, EventUserID: saga.TargetUserID, EventType: "AnonymousClaimDestinationCommitted",
		EventAggregateKind: "anonymous_claim", EventAggregateID: saga.ID,
	}
}
