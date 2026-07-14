package anonymousclaim

import (
	"errors"
	"testing"
)

func TestClaimHappyPathRequiresEveryDeletionReceipt(t *testing.T) {
	saga := Saga{ID: "claim-1", Status: Available, Version: 1}
	var err error
	saga, err = Advance(saga, Input{ExpectedVersion: 1, Command: Reserve, ClaimKey: "key-1", TargetTenantID: "tenant-1", TargetUserID: "user-1", MissionID: "mission-1"})
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
		saga, err = Advance(saga, Input{ExpectedVersion: saga.Version, Command: RecordDeletionReceipt, DeletionReceipt: receipt})
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

func TestReservedClaimCannotExpireOrChangeMission(t *testing.T) {
	saga, _ := Advance(Saga{Status: Available, Version: 1}, Input{ExpectedVersion: 1, Command: Reserve, ClaimKey: "k", TargetTenantID: "t", TargetUserID: "u", MissionID: "m"})
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

func TestTerminalStatesRejectAllOrdinaryCommands(t *testing.T) {
	for _, status := range []Status{Claimed, Expired, ManualReview} {
		if _, err := Advance(Saga{Status: status, Version: 9}, Input{ExpectedVersion: 9, Command: Reserve, ClaimKey: "k", TargetTenantID: "t", TargetUserID: "u", MissionID: "m"}); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("status=%s error=%v", status, err)
		}
	}
}
