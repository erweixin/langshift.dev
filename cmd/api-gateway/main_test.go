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
		{path: "/v1/messages", want: true},
		{path: "/v1/runs/run-1", want: true},
		{path: "/v1/runs/run-1/cancel", want: true},
		{path: "/v1/approvals/approval-1/decisions", want: true},
		{path: "/v1/admin/approval-requests/approval-1/decisions", want: true},
		{path: "/v1/admin/repair-commands", want: true},
		{path: "/v1/admin/repair-commands/repair-1/decisions", want: true},
		{path: "/v1/conversations/", want: false},
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
