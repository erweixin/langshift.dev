package anonymousclaim

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestClaimHappyPathRequiresEveryDeletionReceipt(t *testing.T) {
	saga := Saga{ID: "claim-1", Status: Available, Version: 1}
	var err error
	saga, err = Advance(saga, Input{ExpectedVersion: 1, Command: Reserve, ClaimKey: "key-1", TargetTenantID: "tenant-1", TargetUserID: "user-1", MissionID: "mission-1", OccurredAt: time.Unix(1_800_000_000, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	saga, err = Advance(saga, Input{ExpectedVersion: 2, Command: CommitDestination, MissionID: "mission-1", DestinationCommitEventID: "event-1"})
	if err != nil {
		t.Fatal(err)
	}
	saga, err = Advance(saga, Input{ExpectedVersion: 3, Command: BeginErasing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Advance(saga, Input{ExpectedVersion: 4, Command: Complete}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("missing receipts error=%v", err)
	}
	for _, receipt := range requiredReceipts {
		saga, err = Advance(saga, Input{ExpectedVersion: saga.Version, Command: RecordDeletionReceipt, DeletionReceipt: testReceipt(receipt)})
		if err != nil {
			t.Fatal(err)
		}
	}
	saga, err = Advance(saga, Input{ExpectedVersion: saga.Version, Command: Complete})
	if err != nil {
		t.Fatal(err)
	}
	if saga.Status != Claimed || saga.MissionID != "mission-1" {
		t.Fatalf("unexpected final saga: %#v", saga)
	}
}

func testReceipt(surface string) DeletionReceipt {
	return DeletionReceipt{ID: "receipt-" + surface, Surface: surface, Hash: "hash-" + surface, ErasedAt: time.Unix(1_800_000_000, 0).UTC(), Details: json.RawMessage(`{"verified":true}`)}
}

func TestDeletionReceiptReplayMustMatchOriginalCredential(t *testing.T) {
	saga := Saga{Status: Erasing, Version: 4}
	receipt := testReceipt("body_payload")
	saga, err := Advance(saga, Input{ExpectedVersion: 4, Command: RecordDeletionReceipt, DeletionReceipt: receipt})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Advance(saga, Input{ExpectedVersion: saga.Version, Command: RecordDeletionReceipt, DeletionReceipt: receipt})
	if err != nil || len(replayed.DeletionReceipts) != 1 || replayed.Version != saga.Version {
		t.Fatalf("replay=%#v error=%v", replayed, err)
	}
	tampered := receipt
	tampered.Hash = "different"
	if _, err = Advance(replayed, Input{ExpectedVersion: replayed.Version, Command: RecordDeletionReceipt, DeletionReceipt: tampered}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("tampered replay error=%v", err)
	}
}

func TestReservedClaimCannotExpireOrChangeMission(t *testing.T) {
	saga, _ := Advance(Saga{Status: Available, Version: 1}, Input{ExpectedVersion: 1, Command: Reserve, ClaimKey: "k", TargetTenantID: "t", TargetUserID: "u", MissionID: "m", OccurredAt: time.Unix(1_800_000_000, 0).UTC()})
	if _, err := Advance(saga, Input{ExpectedVersion: 2, Command: Expire}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reserved expiry error=%v", err)
	}
	if _, err := Advance(saga, Input{ExpectedVersion: 2, Command: CommitDestination, MissionID: "other", DestinationCommitEventID: "e"}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("mission mutation error=%v", err)
	}
	if _, err := Advance(saga, Input{ExpectedVersion: 1, Command: CommitDestination, MissionID: "m", DestinationCommitEventID: "e"}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("CAS error=%v", err)
	}
}

func TestExpiryRequiresDeadlineAndCannotRacePastReservation(t *testing.T) {
	expiresAt := time.Unix(1_800_000_000, 0).UTC()
	available := Saga{ID: "claim", Status: Available, Version: 1, ExpiresAt: expiresAt}
	if _, err := Advance(available, Input{ExpectedVersion: 1, Command: Reserve, ClaimKey: "k", TargetTenantID: "t", TargetUserID: "u", MissionID: "m", OccurredAt: expiresAt}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reservation at expiry error=%v", err)
	}
	if _, err := Advance(available, Input{ExpectedVersion: 1, Command: Expire, OccurredAt: expiresAt.Add(-time.Nanosecond)}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("early expiry error=%v", err)
	}
	reserved, err := Advance(available, Input{ExpectedVersion: 1, Command: Reserve, ClaimKey: "k", TargetTenantID: "t", TargetUserID: "u", MissionID: "m", OccurredAt: expiresAt.Add(-time.Nanosecond)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Advance(reserved, Input{ExpectedVersion: reserved.Version, Command: Expire, OccurredAt: expiresAt}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reserved expiry error=%v", err)
	}
	expired, err := Advance(available, Input{ExpectedVersion: 1, Command: Expire, OccurredAt: expiresAt})
	if err != nil || expired.Status != Expired {
		t.Fatalf("expired=%#v error=%v", expired, err)
	}
}

func TestTerminalStatesRejectAllOrdinaryCommands(t *testing.T) {
	for _, status := range []Status{Claimed, Expired, ManualReview} {
		if _, err := Advance(Saga{Status: status, Version: 9}, Input{ExpectedVersion: 9, Command: Reserve, ClaimKey: "k", TargetTenantID: "t", TargetUserID: "u", MissionID: "m"}); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("status=%s error=%v", status, err)
		}
	}
}

func TestValidateSuccessorRejectsCallerCraftedMutation(t *testing.T) {
	current := Saga{ID: "claim", AnonymousSubjectID: "subject", Status: Reserved, Version: 2, ClaimKey: "key", TargetTenantID: "tenant", TargetUserID: "user", MissionID: "mission"}
	valid, err := Advance(current, Input{ExpectedVersion: 2, Command: CommitDestination, MissionID: "mission", DestinationCommitEventID: "event"})
	if err != nil || ValidateSuccessor(current, valid) != nil {
		t.Fatalf("valid successor error=%v", err)
	}
	forged := valid
	forged.TargetUserID = "attacker"
	if err = ValidateSuccessor(current, forged); !errors.Is(err, ErrInvariant) {
		t.Fatalf("forged successor error=%v", err)
	}
}
