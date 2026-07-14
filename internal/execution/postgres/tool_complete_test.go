package postgres

import (
	"bytes"
	"testing"

	"github.com/langshift/lites/internal/execution/statemachine"
)

func TestParallelJoinPoliciesAndContinuationIdentifiers(t *testing.T) {
	tests := []struct {
		name                             string
		policy                           string
		required, quorum, terminal, good int
		want                             bool
	}{
		{"all pending", "all", 2, 2, 1, 1, false},
		{"all terminal with failure", "all", 2, 2, 2, 1, true},
		{"any success", "any", 3, 1, 1, 1, true},
		{"any still possible", "any", 3, 1, 2, 0, false},
		{"any exhausted", "any", 3, 1, 3, 0, true},
		{"quorum reached", "quorum", 4, 3, 3, 3, true},
		{"quorum possible", "quorum", 4, 3, 2, 1, false},
		{"quorum impossible", "quorum", 4, 3, 3, 1, true},
		{"invalid counts", "all", 2, 2, 3, 2, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parallelJoinSatisfied(test.policy, test.required, test.quorum, test.terminal, test.good); got != test.want {
				t.Fatalf("got %v want %v", got, test.want)
			}
		})
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{0xa4}, 32)}
	first, err := store.continuationIdentifiers("group")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.continuationIdentifiers("group")
	if err != nil || first != second {
		t.Fatalf("unstable ids first=%#v second=%#v err=%v", first, second, err)
	}
	seen := map[string]bool{}
	for _, value := range []string{first.continuation, first.command, first.job, first.groupEvent, first.groupOutbox, first.groupPublish, first.runEvent, first.runOutbox, first.runPublish, first.resumeOutbox} {
		if seen[value] {
			t.Fatalf("identifier collision: %s", value)
		}
		seen[value] = true
	}
	if toolCompletionEventType(statemachine.ToolCallSucceeded) != "ToolCallSucceeded" || toolCompletionEventType(statemachine.ToolCallFailed) != "ToolCallFailed" {
		t.Fatal("terminal event mapping drifted")
	}
}
