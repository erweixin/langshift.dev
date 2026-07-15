package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

func TestAcceptFailsClosedBeforeDatabaseAccess(t *testing.T) {
	now := time.Now().UTC()
	valid := AcceptRunCommand{RunID: "run", TenantID: "tenant", UserID: "user", ConversationID: "conversation", CorrelationID: "correlation", DueAt: now.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost":100}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 10, CostUnits: 16, MaxAttempts: 5}
	if _, err := (RunStore{}).Accept(t.Context(), valid); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid store: %v", err)
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{1}, 32), StoreEpoch: "epoch"}
	for name, mutate := range map[string]func(*AcceptRunCommand){
		"scalar budget":          func(command *AcceptRunCommand) { command.BudgetSnapshot = json.RawMessage(`1`) },
		"array actor":            func(command *AcceptRunCommand) { command.Actor = json.RawMessage(`[]`) },
		"missing hash":           func(command *AcceptRunCommand) { command.StartCommand.Hash = "" },
		"negative priority":      func(command *AcceptRunCommand) { command.Priority = -1 },
		"unknown queue":          func(command *AcceptRunCommand) { command.QueueClass = "urgent" },
		"missing resource":       func(command *AcceptRunCommand) { command.ResourceClass = "" },
		"zero cost":              func(command *AcceptRunCommand) { command.CostUnits = 0 },
		"zero attempts":          func(command *AcceptRunCommand) { command.MaxAttempts = 0 },
		"partial child identity": func(command *AcceptRunCommand) { command.ParentRunID = "parent" },
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
	child := valid
	child.ParentRunID, child.RootRunID = "parent", "root"
	child.SpawnToolCallID, child.ChildGroupID = "spawn", "group"
	child.Depth, child.InheritedBudgetMicrounits = 1, 1000
	if !validAcceptRun(child) {
		t.Fatal("complete child Run identity rejected")
	}
	child.Depth = 6
	if validAcceptRun(child) {
		t.Fatal("child Run beyond maximum depth accepted")
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

func TestClaimInputFailsClosedAndIdentifiersAreStable(t *testing.T) {
	valid := ClaimRunCommand{
		Command: eventpostgres.DeliveredCommand{
			TenantID: "tenant", StoreEpoch: "epoch", CommandID: "command",
			CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: "run",
			PayloadRef: "encrypted://command", PayloadHash: "command-hash",
		},
		ConsumerName: "agent-run-worker", WorkerID: "worker-1", CorrelationID: "correlation",
		Actor:               json.RawMessage(`{"kind":"service"}`),
		RunEvent:            PayloadPointer{Ref: "encrypted://run-started", Hash: "run-started"},
		AttemptStartedEvent: PayloadPointer{Ref: "encrypted://attempt-started", Hash: "attempt-started"},
		AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://attempt-expired", Hash: "attempt-expired"},
	}
	if _, err := (RunStore{}).ClaimStart(t.Context(), valid); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid store: %v", err)
	}
	for name, mutate := range map[string]func(*ClaimRunCommand){
		"wrong command type":  func(command *ClaimRunCommand) { command.Command.CommandType = "RunAgent" },
		"wrong aggregate":     func(command *ClaimRunCommand) { command.Command.AggregateKind = "tool_call" },
		"missing worker":      func(command *ClaimRunCommand) { command.WorkerID = "" },
		"scalar actor":        func(command *ClaimRunCommand) { command.Actor = json.RawMessage(`1`) },
		"missing event hash":  func(command *ClaimRunCommand) { command.RunEvent.Hash = "" },
		"missing expiry hash": func(command *ClaimRunCommand) { command.AttemptExpiredEvent.Hash = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if validClaimRun(candidate) {
				t.Fatal("invalid claim accepted")
			}
		})
	}
	for commandType, eventType := range map[string]string{
		"StartAgentRun": "RunStarted", "ResumeAgentRun": "RunResumed", "ResumeParentRun": "RunResumed",
	} {
		candidate := valid
		candidate.Command.CommandType = commandType
		if !validClaimRun(candidate) || claimRunEventType(commandType) != eventType {
			t.Fatalf("command type %s did not map to %s", commandType, eventType)
		}
	}

	store := RunStore{IDKey: bytes.Repeat([]byte{0x42}, 32)}
	first, err := store.claimEventIdentifiers("10000000-0000-4000-8000-000000000009")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.claimEventIdentifiers("10000000-0000-4000-8000-000000000009")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("claim identifiers differ: %#v %#v", first, second)
	}
	seen := map[string]bool{}
	for _, value := range []string{first.runEvent, first.runOutbox, first.runPublish, first.attemptEvent, first.attemptOutbox, first.attemptPublish} {
		if seen[value] {
			t.Fatalf("claim identifier domain collision: %s", value)
		}
		seen[value] = true
	}
}

func TestTerminalRunMappingsAreExhaustive(t *testing.T) {
	for _, test := range []struct {
		state        string
		event        string
		attemptState string
		jobState     string
	}{
		{state: "succeeded", event: "RunSucceeded", attemptState: "succeeded", jobState: "succeeded"},
		{state: "failed", event: "RunFailed", attemptState: "failed", jobState: "failed"},
		{state: "cancelled", event: "RunCancelled", attemptState: "abandoned", jobState: "cancelled"},
		{state: "expired", event: "RunExpired", attemptState: "expired", jobState: "expired"},
	} {
		attemptState, jobState := terminalExecutionStates(statemachine.RunState(test.state))
		if terminalRunEventType(statemachine.RunState(test.state)) != test.event || string(attemptState) != test.attemptState || jobState != test.jobState {
			t.Fatalf("invalid terminal mapping for %s", test.state)
		}
	}
}
