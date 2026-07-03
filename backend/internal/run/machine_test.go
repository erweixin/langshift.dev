package run

import "testing"

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from string
		to   string
		want bool
	}{
		{from: StatusAccepted, to: StatusQueued, want: true},
		{from: StatusQueued, to: StatusExecuting, want: true},
		{from: StatusExecuting, to: StatusSucceeded, want: true},
		{from: StatusAccepted, to: StatusFailed, want: true},
		{from: StatusQueued, to: StatusExpired, want: true},
		{from: StatusSucceeded, to: StatusExecuting, want: false},
		{from: StatusFailed, to: StatusQueued, want: false},
		{from: StatusAccepted, to: StatusSucceeded, want: false},
	}

	for _, test := range tests {
		if got := CanTransition(test.from, test.to); got != test.want {
			t.Fatalf("CanTransition(%q, %q) = %v, want %v", test.from, test.to, got, test.want)
		}
	}
}

func TestTargetStatusForEvent(t *testing.T) {
	target, ok := TargetStatusForEvent(EventRunStarted)
	if !ok {
		t.Fatal("RunStarted target not found")
	}
	if target != StatusExecuting {
		t.Fatalf("RunStarted target = %q, want %q", target, StatusExecuting)
	}

	if _, ok := TargetStatusForEvent("EvidenceSubmitted"); ok {
		t.Fatal("non-run event unexpectedly had a target status")
	}
}
