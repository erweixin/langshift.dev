package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestSpawnChildRunsValidationAndIdentifiersFailClosed(t *testing.T) {
	claim := RunClaim{RunID: "parent", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", RunVersion: 3, CommandID: "command", ConsumerName: "agent-run-worker", RequestHash: "delivery-hash", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "lease", LeaseExpiresAt: time.Now().Add(time.Minute)}
	pointer := func(name string) PayloadPointer { return PayloadPointer{Ref: "encrypted://" + name, Hash: name} }
	child := ChildRunRequest{RunID: "child-1", RequestHash: "request-1", DescriptorSnapshotID: "spawn_agent_run@v1", NormalizedInputRef: "encrypted://input", BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":1000}`), BudgetMicrounits: 1000, DueAt: time.Now().Add(time.Hour), Required: true, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 1, MaxAttempts: 5, ToolSucceededEvent: pointer("tool"), AcceptedEvent: pointer("accepted"), QueuedEvent: pointer("queued"), StartCommand: pointer("start")}
	valid := SpawnChildRunsCommand{Claim: claim, ExpectedRunVersion: 3, StepID: "delegate", JoinPolicy: "all", QuorumCount: 1, Children: []ChildRunRequest{child}, PlanResultHash: "plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", AttemptCompletedEvent: pointer("attempt"), ParentWaitingEvent: pointer("waiting")}
	if !validSpawnChildRuns(valid) {
		t.Fatal("valid child spawn rejected")
	}
	for name, mutate := range map[string]func(*SpawnChildRunsCommand){
		"missing parent event": func(command *SpawnChildRunsCommand) { command.ParentWaitingEvent = PayloadPointer{} },
		"partial quorum":       func(command *SpawnChildRunsCommand) { command.QuorumCount = 2 },
		"invalid profile":      func(command *SpawnChildRunsCommand) { command.Children[0].BehaviorProfile = "unbounded_agent" },
		"zero budget":          func(command *SpawnChildRunsCommand) { command.Children[0].BudgetMicrounits = 0 },
		"child beyond cost":    func(command *SpawnChildRunsCommand) { command.Children[0].CostUnits = 1_000_000_000_001 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Children = append([]ChildRunRequest(nil), valid.Children...)
			mutate(&candidate)
			if validSpawnChildRuns(candidate) {
				t.Fatal("invalid child spawn accepted")
			}
		})
	}
	duplicate := child
	valid.Children = []ChildRunRequest{child, duplicate}
	valid.QuorumCount = 2
	if validSpawnChildRuns(valid) {
		t.Fatal("duplicate child Run accepted")
	}

	store := RunStore{IDKey: bytes.Repeat([]byte{0x43}, 32)}
	groupA, eventA, childrenA, err := store.childSpawnIdentifiers("parent", 3, "delegate", []ChildRunRequest{child})
	if err != nil {
		t.Fatal(err)
	}
	groupB, eventB, childrenB, err := store.childSpawnIdentifiers("parent", 3, "delegate", []ChildRunRequest{child})
	if err != nil || groupA != groupB || eventA != eventB || childrenA[0] != childrenB[0] {
		t.Fatalf("child identifiers are not stable: %v", err)
	}
	seen := map[string]bool{groupA: true}
	for _, value := range []string{eventA.event, eventA.outbox, eventA.publish, childrenA[0].toolCall, childrenA[0].toolEvent, childrenA[0].toolOutbox, childrenA[0].toolPublish} {
		if seen[value] {
			t.Fatalf("identifier domain collision: %s", value)
		}
		seen[value] = true
	}
}

func TestOrchestrationBudgetRequiresPositiveMicrounits(t *testing.T) {
	if value, ok := orchestrationBudget(json.RawMessage(`{"max_cost_microunits":2500}`)); !ok || value != 2500 {
		t.Fatalf("budget=%d ok=%v", value, ok)
	}
	for _, raw := range []string{`{}`, `{"max_cost_microunits":0}`, `{"max_cost_microunits":"2500"}`} {
		if _, ok := orchestrationBudget(json.RawMessage(raw)); ok {
			t.Fatalf("invalid budget accepted: %s", raw)
		}
	}
}
