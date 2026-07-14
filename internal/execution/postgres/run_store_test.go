package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAcceptFailsClosedBeforeDatabaseAccess(t *testing.T) {
	now := time.Now().UTC()
	valid := AcceptRunCommand{RunID: "run", TenantID: "tenant", UserID: "user", ConversationID: "conversation", CorrelationID: "correlation", DueAt: now.Add(time.Hour), ProfileSnapshotID: "profile-v1", BudgetSnapshot: json.RawMessage(`{"max_cost":100}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://start", Hash: "start"}, QueueClass: "interactive", Priority: 10}
	if _, err := (RunStore{}).Accept(t.Context(), valid); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid store: %v", err)
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{1}, 32), StoreEpoch: "epoch"}
	for name, mutate := range map[string]func(*AcceptRunCommand){
		"scalar budget":     func(command *AcceptRunCommand) { command.BudgetSnapshot = json.RawMessage(`1`) },
		"array actor":       func(command *AcceptRunCommand) { command.Actor = json.RawMessage(`[]`) },
		"missing hash":      func(command *AcceptRunCommand) { command.StartCommand.Hash = "" },
		"negative priority": func(command *AcceptRunCommand) { command.Priority = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if validAcceptRun(candidate) {
				t.Fatal("invalid command accepted")
			}
			_ = store
		})
	}
}

func TestRunIdentifiersAreStableAndDomainSeparated(t *testing.T) {
	store := RunStore{IDKey: bytes.Repeat([]byte{0x63}, 32)}
	first, err := store.identifiers("10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.identifiers("10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("deterministic identifiers differ: %#v %#v", first, second)
	}
	seen := map[string]bool{}
	for _, value := range []string{first.acceptedEvent, first.acceptedPublishOutbox, first.acceptedPublishCommand, first.queuedEvent, first.queuedPublishOutbox, first.queuedPublishCommand, first.startOutbox, first.startCommand, first.startJob} {
		if seen[value] {
			t.Fatalf("identifier domain collision: %s", value)
		}
		seen[value] = true
	}
}
