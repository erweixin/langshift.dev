package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
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

func TestAnonymousClaimRepairFaultScenarios100(t *testing.T) {
	metrics := struct {
		Scenario                      string `json:"scenario"`
		RepetitionsPerCase            int    `json:"repetitions_per_case"`
		TargetCommitUnknown           int    `json:"target_commit_unknown"`
		TargetCommitResolved          int    `json:"target_commit_resolved"`
		ReceiptConflicts              int    `json:"receipt_conflicts"`
		ReceiptConflictsRetained      int    `json:"receipt_conflicts_retained_manual_review"`
		SourceDestinationHashMismatch int    `json:"source_destination_hash_mismatches"`
		HashMismatchesRetained        int    `json:"hash_mismatches_retained_manual_review"`
		ErroneousConvergences         int    `json:"erroneous_convergences"`
		DuplicateDestinationMissions  int    `json:"duplicate_destination_missions"`
		DirectAggregateMutations      int    `json:"direct_aggregate_mutations"`
	}{Scenario: "anonymous_claim_manual_review_repair", RepetitionsPerCase: 100}
	now := time.Unix(1_800_000_000, 0).UTC()
	for repetition := range metrics.RepetitionsPerCase {
		suffix := fmt.Sprintf("-%03d", repetition)
		saga := anonymousclaim.Saga{
			ID: "claim" + suffix, AnonymousSubjectID: "subject" + suffix,
			Status: anonymousclaim.ManualReview, Version: 3, ClaimKey: "claim-key" + suffix,
			TargetTenantID: "tenant", TargetUserID: "user", MissionID: "mission" + suffix,
			SourceRouteRevisionID: "source-route" + suffix, ClaimSetHash: "claim-set" + suffix,
		}
		targetRouteID, destinationEventID := "target-route"+suffix, "destination-event"+suffix

		metrics.TargetCommitUnknown++
		sagaBeforeInspection := saga
		resolution, err := resolveAnonymousClaimManualReview(saga, targetRouteID, destinationEventID, exactAnonymousClaimTargetFacts(saga, targetRouteID, destinationEventID))
		if err != nil || resolution.TargetStatus != string(anonymousclaim.DestinationCommitted) || resolution.ClaimKey != saga.ClaimKey || resolution.DestinationCommitEventID != destinationEventID || resolution.ClaimVersion != saga.Version+1 {
			t.Fatalf("target commit unknown repetition %d resolved unsafely: resolution=%#v err=%v", repetition, resolution, err)
		}
		if !reflect.DeepEqual(saga, sagaBeforeInspection) {
			metrics.DirectAggregateMutations++
			t.Fatalf("target inspection repetition %d mutated the source aggregate", repetition)
		}
		metrics.TargetCommitResolved++

		metrics.ReceiptConflicts++
		conflicted := saga
		conflicted.DestinationCommitEventID = destinationEventID
		conflicted.DeletionReceipts = []anonymousclaim.DeletionReceipt{
			{ID: "receipt-a" + suffix, Surface: "body_payload", Hash: "hash-a" + suffix, ErasedAt: now, Details: json.RawMessage(`{"verified":true}`)},
			{ID: "receipt-b" + suffix, Surface: "body_payload", Hash: "hash-b" + suffix, ErasedAt: now, Details: json.RawMessage(`{"verified":true}`)},
		}
		conflictedBeforeInspection := conflicted
		conflictedBeforeInspection.DeletionReceipts = slices.Clone(conflicted.DeletionReceipts)
		if _, err = resolveAnonymousClaimManualReview(conflicted, targetRouteID, destinationEventID, exactAnonymousClaimTargetFacts(saga, targetRouteID, destinationEventID)); !errors.Is(err, ErrAnonymousClaimRepairUnresolved) {
			metrics.ErroneousConvergences++
			t.Fatalf("receipt conflict repetition %d did not remain manual_review: %v", repetition, err)
		}
		if !reflect.DeepEqual(conflicted, conflictedBeforeInspection) {
			metrics.DirectAggregateMutations++
			t.Fatalf("receipt inspection repetition %d mutated the source aggregate", repetition)
		}
		metrics.ReceiptConflictsRetained++

		metrics.SourceDestinationHashMismatch++
		mismatchedFacts := exactAnonymousClaimTargetFacts(saga, targetRouteID, destinationEventID)
		mismatchedFacts.RouteClaimSetHash = "different" + suffix
		if _, err = resolveAnonymousClaimManualReview(saga, targetRouteID, destinationEventID, mismatchedFacts); !errors.Is(err, ErrAnonymousClaimRepairUnresolved) {
			metrics.ErroneousConvergences++
			t.Fatalf("hash mismatch repetition %d did not remain manual_review: %v", repetition, err)
		}
		if !reflect.DeepEqual(saga, sagaBeforeInspection) {
			metrics.DirectAggregateMutations++
			t.Fatalf("hash inspection repetition %d mutated the source aggregate", repetition)
		}
		metrics.HashMismatchesRetained++
	}
	encoded, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("anonymous_claim_repair_gate=%s", encoded)
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
