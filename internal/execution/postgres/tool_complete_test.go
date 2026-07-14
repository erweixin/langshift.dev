package postgres

import (
	"bytes"
	"testing"
	"time"

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
	if toolCompletionEventType(statemachine.ToolCallSucceeded) != "ToolCallSucceeded" || toolCompletionEventType(statemachine.ToolCallFailed) != "ToolCallFailed" || toolCompletionEventType(statemachine.ToolCallOutcomeUnknown) != "ToolCallOutcomeUnknown" {
		t.Fatal("terminal event mapping drifted")
	}
}

func TestEffectCompletionValues(t *testing.T) {
	now := time.Date(2026, time.July, 15, 2, 0, 0, 0, time.UTC)
	status, confirmedAt, dueAt, resource, valid := effectCompletionValues(statemachine.ToolCallSucceeded, EffectCompletion{ExternalResourceRef: "provider://resource/1"}, now)
	if !valid || status != "confirmed" || confirmedAt == nil || *confirmedAt != now || dueAt != nil || resource != "provider://resource/1" {
		t.Fatalf("confirmed=%s/%v/%v/%s/%v", status, confirmedAt, dueAt, resource, valid)
	}
	status, confirmedAt, dueAt, _, valid = effectCompletionValues(statemachine.ToolCallOutcomeUnknown, EffectCompletion{ReconciliationDueAt: now.Add(time.Minute)}, now)
	if !valid || status != "outcome_unknown" || confirmedAt != nil || dueAt == nil || *dueAt != now.Add(time.Minute) {
		t.Fatalf("unknown=%s/%v/%v/%v", status, confirmedAt, dueAt, valid)
	}
	if _, _, _, _, valid = effectCompletionValues(statemachine.ToolCallOutcomeUnknown, EffectCompletion{ReconciliationDueAt: now}, now); valid {
		t.Fatal("non-future reconciliation deadline accepted")
	}
	if _, _, _, _, valid = effectCompletionValues(statemachine.ToolCallFailed, EffectCompletion{ExternalResourceRef: "provider://unexpected"}, now); valid {
		t.Fatal("definitive failure accepted an external resource")
	}
	for _, class := range []string{"idempotent_write", "reconcilable_write", "compensatable_write", "irreversible_write"} {
		if !isWriteEffectClass(class) {
			t.Fatalf("write effect class rejected: %s", class)
		}
	}
	if isWriteEffectClass("read_only") || isWriteEffectClass("invented") {
		t.Fatal("non-write effect class accepted")
	}
}
