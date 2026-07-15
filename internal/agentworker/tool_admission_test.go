package agentworker

import "testing"

func TestToolPermissionsUseReviewedRBACAndFailClosed(t *testing.T) {
	if !allowsToolPermissions("tenant", "user", "member", []string{"private_work.read", "private_work.update"}) {
		t.Fatal("reviewed member permissions denied")
	}
	for _, permissions := range [][]string{{}, {"tools.read"}, {"private_work.approve"}, {"private_work.read.extra"}} {
		if allowsToolPermissions("tenant", "user", "member", permissions) {
			t.Fatalf("unreviewed permissions allowed: %#v", permissions)
		}
	}
	if allowsToolPermissions("tenant", "user", "reviewer", []string{"private_work.read"}) {
		t.Fatal("reviewer gained private work permission")
	}
}
