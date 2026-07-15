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
