package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestToolPlanValidationAndIdentifiers(t *testing.T) {
	claim := RunClaim{RunID: "run", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", RunVersion: 3, CommandID: "command", ConsumerName: "agent", RequestHash: "start-hash", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "token", LeaseExpiresAt: time.Now().Add(time.Minute)}
	request := ToolRequest{ToolName: "web_search", DescriptorSnapshotID: "web_search@v1", NormalizedInputRef: "encrypted://input", RequestHash: "request-one", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2, MaxAttempts: 3, RequestedEvent: PayloadPointer{Ref: "encrypted://event", Hash: "event"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://command", Hash: "command"}}
	valid := RequestToolsCommand{Claim: claim, ExpectedRunVersion: 3, StepID: "step-1", JoinPolicy: "all", QuorumCount: 1, ToolRequests: []ToolRequest{request}, PlanResultHash: "plan-result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://attempt", Hash: "attempt"}}
	if !validRequestTools(valid) {
		t.Fatal("valid tool plan rejected")
	}
	for name, mutate := range map[string]func(*RequestToolsCommand){
		"no required": func(command *RequestToolsCommand) { command.ToolRequests[0].Required = false },
		"bad quorum":  func(command *RequestToolsCommand) { command.QuorumCount = 2 },
		"write no key": func(command *RequestToolsCommand) {
			command.ToolRequests[0].EffectClass = "idempotent_write"
		},
		"write no scope": func(command *RequestToolsCommand) {
			command.ToolRequests[0].EffectClass = "idempotent_write"
			command.ToolRequests[0].EffectKey = "effect"
			command.ToolRequests[0].ProviderID = "provider"
		},
		"read with key":      func(command *RequestToolsCommand) { command.ToolRequests[0].EffectKey = "unexpected" },
		"read with provider": func(command *RequestToolsCommand) { command.ToolRequests[0].ProviderID = "unexpected" },
		"unknown queue": func(command *RequestToolsCommand) {
			command.ToolRequests[0].QueueClass = "urgent"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.ToolRequests = append([]ToolRequest(nil), valid.ToolRequests...)
			mutate(&candidate)
			if validRequestTools(candidate) {
				t.Fatal("invalid tool plan accepted")
			}
		})
	}
	duplicate := valid
	duplicate.ToolRequests = []ToolRequest{request, request}
	duplicate.QuorumCount = 2
	if validRequestTools(duplicate) {
		t.Fatal("duplicate request hash accepted")
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{0x91}, 32)}
	first, firstGroup, err := store.toolRequestIdentifiers(claim.RunID, claim.RunVersion, valid.ToolRequests)
	if err != nil {
		t.Fatal(err)
	}
	second, secondGroup, err := store.toolRequestIdentifiers(claim.RunID, claim.RunVersion, valid.ToolRequests)
	if err != nil || firstGroup != secondGroup || first[0] != second[0] {
		t.Fatalf("unstable identifiers first=%#v/%s second=%#v/%s error=%v", first, firstGroup, second, secondGroup, err)
	}
	seen := map[string]bool{firstGroup: true}
	for _, value := range []string{first[0].toolCall, first[0].effect, first[0].command, first[0].job, first[0].event, first[0].publishOutbox, first[0].publishCommand, first[0].executeOutbox} {
		if seen[value] {
			t.Fatalf("identifier domain collision: %s", value)
		}
		seen[value] = true
	}
}
