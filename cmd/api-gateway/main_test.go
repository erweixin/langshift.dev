package main

import "testing"

func TestRealtimeRoutingIsExactAndCannotCaptureLookalikePaths(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/events", want: true},
		{path: "/v1/realtime", want: true},
		{path: "/v1/events/", want: false},
		{path: "/v1/realtime/", want: false},
		{path: "/v1/events/export", want: false},
		{path: "/v1/realtime-admin", want: false},
		{path: "/V1/realtime", want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := isRealtimeRoute(test.path); got != test.want {
				t.Fatalf("isRealtimeRoute(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestBehaviorRoutingIsExactAndCannotCaptureLookalikePaths(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/admin/behavior/snapshots", want: true},
		{path: "/v1/admin/behavior/evaluations", want: true},
		{path: "/v1/admin/behavior/promotions", want: true},
		{path: "/v1/admin/behavior/channels/coach/production", want: true},
		{path: "/v1/admin/behavior/channels/route_planner/staging", want: true},
		{path: "/v1/admin/behavior/channels/coach/production/", want: false},
		{path: "/v1/admin/behavior/channels/unknown/production", want: false},
		{path: "/v1/admin/behavior/snapshots/export", want: false},
		{path: "/v1/admin/behavior-rollbacks", want: false},
		{path: "/internal/v1/behavior/rollbacks", want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := isBehaviorRoute(test.path); got != test.want {
				t.Fatalf("isBehaviorRoute(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestAgentRoutingIsExactAndCannotCaptureLookalikePaths(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/conversations", want: true},
		{path: "/v1/conversations/conversation-1", want: true},
		{path: "/v1/messages", want: true},
		{path: "/v1/runs/run-1", want: true},
		{path: "/v1/runs/run-1/cancel", want: true},
		{path: "/v1/approvals/approval-1/decisions", want: true},
		{path: "/v1/admin/approval-requests/approval-1/decisions", want: true},
		{path: "/v1/admin/repair-commands", want: true},
		{path: "/v1/admin/repair-commands/repair-1/decisions", want: true},
		{path: "/v1/conversations/", want: false},
		{path: "/v1/conversations/conversation-1/messages", want: false},
		{path: "/v1/messages/export", want: false},
		{path: "/v1/runs/run-1/cancel/extra", want: false},
		{path: "/v1/approvals/approval-1", want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := isAgentRoute(test.path); got != test.want {
				t.Fatalf("isAgentRoute(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestContractRoutingIsExactAndCannotCaptureLookalikePaths(t *testing.T) {
	id := "10000000-0000-4000-8000-000000000001"
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/admin/contracts", want: true},
		{path: "/v1/admin/contracts/" + id + "/approval-decisions", want: true},
		{path: "/v1/admin/entitlements", want: true},
		{path: "/v1/admin/entitlements/" + id + "/approval-decisions", want: true},
		{path: "/v1/admin/usage", want: true},
		{path: "/v1/admin/usage/adjustments", want: true},
		{path: "/v1/admin/audit", want: true},
		{path: "/v1/admin/audit-exports", want: true},
		{path: "/v1/admin/audit-exports/" + id, want: true},
		{path: "/v1/admin/usage/adjustments/" + id + "/approval-decisions", want: true},
		{path: "/v1/admin/contracts/", want: false},
		{path: "/v1/admin/contracts/not-a-uuid/approval-decisions", want: false},
		{path: "/v1/admin/contracts/" + id, want: false},
		{path: "/v1/admin/contracts/" + id + "/approval-decisions/extra", want: false},
		{path: "/v1/admin/entitlements/", want: false},
		{path: "/v1/admin/entitlements/not-a-uuid/approval-decisions", want: false},
		{path: "/v1/admin/usage/adjustments/", want: false},
		{path: "/v1/admin/usage/export", want: false},
		{path: "/v1/admin/audit-exports/", want: false},
		{path: "/v1/admin/audit-exports/not-a-uuid", want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := isContractRoute(test.path); got != test.want {
				t.Fatalf("isContractRoute(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestProductAggregateSnapshotRoutingIsExact(t *testing.T) {
	if !isProductRoute("/v1/admin/aggregate-snapshots") {
		t.Fatal("aggregate snapshot route was not forwarded to Product Service")
	}
	for _, path := range []string{"/v1/admin/aggregate-snapshots/", "/v1/admin/aggregate-snapshots/export"} {
		if isProductRoute(path) {
			t.Fatalf("lookalike route %q was accepted", path)
		}
	}
}

func TestProductRoutingIsExactAndCannotCaptureLookalikePaths(t *testing.T) {
	id := "10000000-0000-4000-8000-000000000001"
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/missions", want: true},
		{path: "/v1/public/status", want: true},
		{path: "/v1/catalog/roles", want: true},
		{path: "/v1/missions/" + id, want: true},
		{path: "/v1/missions/" + id + "/focus", want: true},
		{path: "/v1/route-revisions", want: true},
		{path: "/v1/route-revisions/" + id + "/accept", want: true},
		{path: "/v1/daily-tasks", want: true},
		{path: "/v1/daily-tasks/" + id, want: true},
		{path: "/v1/submissions", want: true},
		{path: "/v1/reviews", want: true},
		{path: "/v1/reviews/" + id, want: true},
		{path: "/v1/capability-evidence", want: true},
		{path: "/v1/capability-claims", want: true},
		{path: "/v1/capability-claims/" + id + "/revisions", want: true},
		{path: "/v1/preferences", want: true},
		{path: "/v1/byok-credentials", want: true},
		{path: "/v1/byok-credentials/" + id, want: true},
		{path: "/v1/memory-policy", want: true},
		{path: "/v1/reminder-schedules", want: true},
		{path: "/v1/reminder-schedules/" + id, want: true},
		{path: "/v1/projects", want: true},
		{path: "/v1/projects/" + id, want: true},
		{path: "/v1/projects/" + id + "/milestones", want: true},
		{path: "/v1/projects/" + id + "/milestones/" + id, want: true},
		{path: "/v1/projects/" + id + "/workspace", want: true},
		{path: "/v1/projects/" + id + "/completion", want: true},
		{path: "/v1/projects/" + id + "/test-runs", want: true},
		{path: "/v1/artifacts", want: true},
		{path: "/v1/artifacts/" + id + "/revisions", want: true},
		{path: "/v1/portfolio-exports", want: true},
		{path: "/v1/portfolio-exports/" + id, want: true},
		{path: "/v1/portfolio-exports/" + id + "/download", want: true},
		{path: "/v1/share-grants", want: true},
		{path: "/v1/share-grants/" + id, want: true},
		{path: "/v1/support/cases", want: true},
		{path: "/v1/support/cases/" + id, want: true},
		{path: "/v1/support/cases/" + id + "/messages", want: true},
		{path: "/v1/admin/aggregate-queries", want: true},
		{path: "/v1/admin/programs", want: true},
		{path: "/v1/admin/programs/" + id, want: true},
		{path: "/v1/admin/cohorts", want: true},
		{path: "/v1/admin/cohorts/" + id + "/enrollments", want: true},
		{path: "/v1/admin/cohorts/" + id + "/enrollments/" + id, want: true},
		{path: "/v1/admin/role-packs", want: true},
		{path: "/v1/admin/task-packs", want: true},
		{path: "/v1/missions/", want: false},
		{path: "/v1/public/status/", want: false},
		{path: "/v1/missions/not-a-uuid", want: false},
		{path: "/v1/missions/" + id + "/focus/extra", want: false},
		{path: "/v1/missions/" + id + "/archive", want: false},
		{path: "/v1/missions/" + id + "/", want: false},
		{path: "/v1/route-revisions/", want: false},
		{path: "/v1/route-revisions/not-a-uuid/accept", want: false},
		{path: "/v1/route-revisions/" + id, want: false},
		{path: "/v1/route-revisions/" + id + "/accept/extra", want: false},
		{path: "/v1/capability-claims/not-a-uuid/revisions", want: false},
		{path: "/v1/capability-claims/" + id, want: false},
		{path: "/v1/byok-credentials/not-a-uuid", want: false},
		{path: "/v1/daily-tasks/", want: false},
		{path: "/v1/daily-tasks/not-a-uuid", want: false},
		{path: "/v1/daily-tasks/" + id + "/extra", want: false},
		{path: "/v1/reminder-schedules/", want: false},
		{path: "/v1/reminder-schedules/not-a-uuid", want: false},
		{path: "/v1/reminder-schedules/" + id + "/extra", want: false},
		{path: "/v1/projects/", want: false},
		{path: "/v1/projects/not-a-uuid", want: false},
		{path: "/v1/projects/" + id + "/unknown", want: false},
		{path: "/v1/projects/" + id + "/workspace/extra", want: false},
		{path: "/v1/projects/" + id + "/milestones/not-a-uuid", want: false},
		{path: "/v1/artifacts/", want: false},
		{path: "/v1/artifacts/not-a-uuid/revisions", want: false},
		{path: "/v1/artifacts/" + id + "/revisions/extra", want: false},
		{path: "/v1/portfolio-exports/", want: false},
		{path: "/v1/portfolio-exports/" + id + "/extra", want: false},
		{path: "/v1/portfolio-exports/" + id + "/download/extra", want: false},
		{path: "/v1/share-grants/", want: false},
		{path: "/v1/share-grants/" + id + "/extra", want: false},
		{path: "/v1/support/cases/", want: false},
		{path: "/v1/support/cases/not-a-uuid", want: false},
		{path: "/v1/support/cases/" + id + "/messages/extra", want: false},
		{path: "/v1/admin/aggregate-queries/", want: false},
		{path: "/v1/admin/programs/", want: false},
		{path: "/v1/admin/programs/not-a-uuid", want: false},
		{path: "/v1/admin/cohorts/", want: false},
		{path: "/v1/admin/cohorts/" + id, want: false},
		{path: "/v1/admin/cohorts/" + id + "/enrollments/extra/extra", want: false},
		{path: "/v1/admin/role-packs/", want: false},
		{path: "/v1/admin/task-packs/", want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := isProductRoute(test.path); got != test.want {
				t.Fatalf("isProductRoute(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}
