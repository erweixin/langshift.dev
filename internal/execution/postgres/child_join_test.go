package postgres

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/execution/statemachine"
)

func TestChildRunCompletionValidationAndIdentifiersFailClosed(t *testing.T) {
	pointer := func(name string) PayloadPointer { return PayloadPointer{Ref: "encrypted://" + name, Hash: name} }
	completion := ChildRunCompletion{ResultSummary: PayloadPointer{Ref: "encrypted://summary", Hash: strings.Repeat("a", 64)}, CompletedEvent: pointer("completed"), GroupJoinedEvent: pointer("joined"), RunResumeQueuedEvent: pointer("queued"), ResumeCommand: pointer("resume"), ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
	if !validChildRunCompletion(completion) {
		t.Fatal("valid child completion rejected")
	}
	for name, mutate := range map[string]func(*ChildRunCompletion){
		"non sha summary":     func(value *ChildRunCompletion) { value.ResultSummary.Hash = "summary" },
		"missing child event": func(value *ChildRunCompletion) { value.CompletedEvent = PayloadPointer{} },
		"missing group event": func(value *ChildRunCompletion) { value.GroupJoinedEvent = PayloadPointer{} },
		"invalid queue":       func(value *ChildRunCompletion) { value.ResumeQueueClass = "urgent" },
		"zero cost":           func(value *ChildRunCompletion) { value.ResumeCostUnits = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := completion
			mutate(&candidate)
			if validChildRunCompletion(candidate) {
				t.Fatal("invalid child completion accepted")
			}
		})
	}

	claim := RunClaim{RunID: "child", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", RunVersion: 3, CommandID: "command", ConsumerName: "worker", RequestHash: "request", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "lease", LeaseExpiresAt: time.Now().Add(time.Minute)}
	command := CompleteRunCommand{Claim: claim, ExpectedRunVersion: 3, TargetState: statemachine.RunSucceeded, ResultHash: "result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", AttemptCompletedEvent: pointer("attempt-completed"), Child: &completion}
	if !validCompleteRun(command) {
		t.Fatal("valid child terminal command rejected")
	}
	command.RunEvent = pointer("ambiguous-root-event")
	if validCompleteRun(command) {
		t.Fatal("ambiguous root and child terminal events accepted")
	}

	store := RunStore{IDKey: bytes.Repeat([]byte{0x55}, 32)}
	first, err := store.childContinuationIdentifiers("group")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.childContinuationIdentifiers("group")
	if err != nil || first != second {
		t.Fatalf("child continuation identifiers are not stable: %v", err)
	}
	tool, err := store.continuationIdentifiers("group")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, value := range []string{first.continuation, first.command, first.job, first.groupEvent, first.groupOutbox, first.groupPublish, first.runEvent, first.runOutbox, first.runPublish, first.resumeOutbox, tool.continuation, tool.command} {
		if seen[value] {
			t.Fatalf("child continuation identifier collision: %s", value)
		}
		seen[value] = true
	}
}
