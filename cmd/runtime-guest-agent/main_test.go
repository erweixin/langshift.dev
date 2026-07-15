package main

import "testing"

func TestGuestAgentRequiresDedicatedUnprivilegedIdentity(t *testing.T) {
	if err := validateIdentity(guestUserID, guestGroupID); err != nil {
		t.Fatal(err)
	}
	for _, identity := range [][2]int{{0, 0}, {guestUserID, 0}, {0, guestGroupID}, {2000, 2000}} {
		if err := validateIdentity(identity[0], identity[1]); err == nil {
			t.Fatalf("accepted identity %v", identity)
		}
	}
}
