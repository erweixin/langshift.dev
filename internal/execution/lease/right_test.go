package lease

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/langshift/lites/internal/security/opaque"
)

func TestVerifierRequiresTheEntireExactUnexpiredExecutionRight(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	manager := opaque.Manager{Purpose: "worker-lease-v1", Pepper: bytes.Repeat([]byte{0x5a}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))}
	credential, err := manager.Issue()
	if err != nil {
		t.Fatal(err)
	}
	stored := StoredRight{
		CommandID: "command-1", AttemptID: "attempt-1", Fence: 17,
		LeaseTokenHash: credential.Digest, LeaseExpiresAt: now.Add(time.Minute),
	}
	presented := PresentedRight{CommandID: "command-1", AttemptID: "attempt-1", Fence: 17, LeaseToken: credential.Raw}
	verifier := Verifier{Tokens: manager, Now: func() time.Time { return now }}
	if err = verifier.Verify(stored, presented); err != nil {
		t.Fatalf("valid right rejected: %v", err)
	}

	tests := []struct {
		name      string
		stored    StoredRight
		presented PresentedRight
		now       time.Time
		want      error
	}{
		{name: "wrong command", stored: stored, presented: PresentedRight{CommandID: "command-2", AttemptID: presented.AttemptID, Fence: presented.Fence, LeaseToken: presented.LeaseToken}, now: now, want: ErrStaleRight},
		{name: "wrong attempt", stored: stored, presented: PresentedRight{CommandID: presented.CommandID, AttemptID: "attempt-2", Fence: presented.Fence, LeaseToken: presented.LeaseToken}, now: now, want: ErrStaleRight},
		{name: "lower fence", stored: stored, presented: PresentedRight{CommandID: presented.CommandID, AttemptID: presented.AttemptID, Fence: 16, LeaseToken: presented.LeaseToken}, now: now, want: ErrStaleRight},
		{name: "higher fence", stored: stored, presented: PresentedRight{CommandID: presented.CommandID, AttemptID: presented.AttemptID, Fence: 18, LeaseToken: presented.LeaseToken}, now: now, want: ErrStaleRight},
		{name: "wrong token", stored: stored, presented: PresentedRight{CommandID: presented.CommandID, AttemptID: presented.AttemptID, Fence: presented.Fence, LeaseToken: "Q0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0M"}, now: now, want: ErrStaleRight},
		{name: "malformed token", stored: stored, presented: PresentedRight{CommandID: presented.CommandID, AttemptID: presented.AttemptID, Fence: presented.Fence, LeaseToken: "not-a-token"}, now: now, want: ErrInvalidRight},
		{name: "exact expiry", stored: stored, presented: presented, now: stored.LeaseExpiresAt, want: ErrExpiredRight},
		{name: "after expiry", stored: stored, presented: presented, now: stored.LeaseExpiresAt.Add(time.Nanosecond), want: ErrExpiredRight},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := verifier
			candidate.Now = func() time.Time { return test.now }
			if got := candidate.Verify(test.stored, test.presented); !errors.Is(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestVerifierRejectsIncompleteStoredOrPresentedRights(t *testing.T) {
	manager := opaque.Manager{Purpose: "worker-lease-v1", Pepper: bytes.Repeat([]byte{0x5a}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))}
	credential, err := manager.Issue()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	validStored := StoredRight{CommandID: "command", AttemptID: "attempt", Fence: 1, LeaseTokenHash: credential.Digest, LeaseExpiresAt: now.Add(time.Minute)}
	validPresented := PresentedRight{CommandID: "command", AttemptID: "attempt", Fence: 1, LeaseToken: credential.Raw}
	verifier := Verifier{Tokens: manager, Now: func() time.Time { return now }}

	invalidStored := validStored
	invalidStored.LeaseTokenHash = [32]byte{}
	if err = verifier.Verify(invalidStored, validPresented); !errors.Is(err, ErrInvalidRight) {
		t.Fatalf("zero stored token digest: %v", err)
	}
	invalidPresented := validPresented
	invalidPresented.AttemptID = ""
	if err = verifier.Verify(validStored, invalidPresented); !errors.Is(err, ErrInvalidRight) {
		t.Fatalf("empty presented attempt: %v", err)
	}
}
