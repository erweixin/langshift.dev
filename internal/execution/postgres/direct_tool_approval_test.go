package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDirectToolProposalHashBindsFrozenExecutionScope(t *testing.T) {
	request := validDirectApprovalRequest()
	first, err := directToolProposalHash(request, "membership:m1:v1:role:member")
	if err != nil || len(first) != 64 {
		t.Fatalf("hash=%q error=%v", first, err)
	}
	request.ExecuteCommand.Hash = strings.Repeat("d", 64)
	second, err := directToolProposalHash(request, "membership:m1:v1:role:member")
	if err != nil || first == second {
		t.Fatalf("execute command substitution was not bound: first=%q second=%q err=%v", first, second, err)
	}
}

func TestDirectToolProposalValidationFailsClosed(t *testing.T) {
	claim := RunClaim{RunID: "run", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", RunVersion: 3, CommandID: "command", ConsumerName: "agent", RequestHash: "request", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "lease", LeaseExpiresAt: time.Now().Add(time.Minute)}
	request := validDirectApprovalRequest()
	permission := "membership:m1:v1:role:member"
	request.ProposalHash, _ = directToolProposalHash(request, permission)
	command := ProposeDirectToolsCommand{
		Claim: claim, ExpectedRunVersion: claim.RunVersion, StepID: "dangerous-write",
		ToolRequests: []DirectApprovalToolRequest{request}, PermissionSnapshot: permission,
		ApprovalExpiresAt: time.Now().Add(time.Hour), PlanResultHash: "provider-result",
		Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation",
		RunWaitingApprovalEvent: directPointer("run-waiting"), AttemptCompletedEvent: directPointer("attempt"),
		NotifyQueueClass: "interactive", NotifyResourceClass: "notification", NotifyPriority: 50,
		NotifyCostUnits: 1, NotifyMaxAttempts: 5,
	}
	if !validProposeDirectTools(command) {
		t.Fatal("valid direct proposal rejected")
	}
	invalid := command
	invalid.ToolRequests = append([]DirectApprovalToolRequest(nil), command.ToolRequests...)
	invalid.ToolRequests[0].ExecuteCommand.Hash = "not-a-digest"
	if validProposeDirectTools(invalid) {
		t.Fatal("unbound execute command accepted")
	}
	invalid = command
	invalid.PermissionSnapshot = ""
	if validProposeDirectTools(invalid) {
		t.Fatal("missing permission snapshot accepted")
	}
}

func validDirectApprovalRequest() DirectApprovalToolRequest {
	return DirectApprovalToolRequest{
		ToolName: "github_create_issue", DescriptorSnapshotID: "github_create_issue@1",
		NormalizedInputRef: "encrypted://input", RequestHash: strings.Repeat("1", 64),
		EffectClass: "idempotent_write", EffectKey: "issue:42", EffectScope: "tenant:github:installation:7", ProviderID: "github",
		PolicySnapshot: "policy:v1", ScopeSnapshot: PayloadPointer{Ref: "encrypted://scope", Hash: strings.Repeat("2", 64)},
		ExecuteCommand: PayloadPointer{Ref: "encrypted://execute", Hash: strings.Repeat("3", 64)},
		QueueClass:     "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 5,
		ToolProposedEvent: directPointer("proposed"), ApprovalRequestedEvent: directPointer("approval"), NotifyApproval: directPointer("notify"),
	}
}

func directPointer(name string) PayloadPointer {
	return PayloadPointer{Ref: "encrypted://direct/" + name, Hash: name}
}
