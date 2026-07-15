package agentworker

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolworker"
)

func TestProductionResultPlanBuilderCreatesToolWorkerPayload(t *testing.T) {
	payloads := &memoryPayloads{}
	builder := productionPlanBuilder(payloads)
	execution, turn := planExecution()
	call := workerPlanCall(false, false)
	plan, err := builder.BuildTools(t.Context(), execution, turn, MessageDocument{}, []ValidatedToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	if plan.JoinPolicy != "all" || plan.QuorumCount != 1 || plan.PlanResultHash != turn.Provider.ResponseHash || plan.AttemptCompletedEvent.Hash == "" || len(plan.ToolRequests) != 1 {
		t.Fatalf("plan=%#v", plan)
	}
	request := plan.ToolRequests[0]
	planned, err := executionpostgres.ToolRequestPlanningIDs(builder.IDKey, execution.Claim.RunID, execution.Claim.RunVersion, 0, call.RequestHash, false)
	if err != nil {
		t.Fatal(err)
	}
	if request.ToolName != call.Tool.Name || request.DescriptorSnapshotID != call.Tool.DescriptorSnapshotID || request.EffectClass != "read_only" || request.NormalizedInputRef == "" || request.ExecuteCommand.Hash == "" || request.PreviewCommand != (executionpostgres.PayloadPointer{}) {
		t.Fatalf("request=%#v", request)
	}
	command := loadToolCommand(t, payloads, execution.Claim.TenantID, planned.CommandID, "tool-execute-command", request.ExecuteCommand)
	if command.ToolCallID != planned.ToolCallID || command.RunID != execution.Claim.RunID || command.DescriptorHash != call.Tool.DescriptorHash || command.RequestHash != call.RequestHash || command.NormalizedInputHash != sha256Hex(call.Input) || command.Input.Ref != request.NormalizedInputRef {
		t.Fatalf("command=%#v", command)
	}
	input, err := payloads.Get(t.Context(), payload.Descriptor{TenantID: execution.Claim.TenantID, ObjectID: planned.ToolCallID, Class: "tool-normalized-input", ContentType: "application/json"}, command.Input)
	if err != nil || string(input) != string(call.Input) {
		t.Fatalf("input=%s err=%v", input, err)
	}
}

func TestProductionResultPlanBuilderCreatesPreviewCommandAndRejectsMixedGroup(t *testing.T) {
	payloads := &memoryPayloads{}
	builder := productionPlanBuilder(payloads)
	execution, turn := planExecution()
	preview := workerPlanCall(true, true)
	plan, err := builder.BuildTools(t.Context(), execution, turn, MessageDocument{}, []ValidatedToolCall{preview})
	if err != nil {
		t.Fatal(err)
	}
	request := plan.ToolRequests[0]
	planned, err := executionpostgres.ToolRequestPlanningIDs(builder.IDKey, execution.Claim.RunID, execution.Claim.RunVersion, 0, preview.RequestHash, true)
	if err != nil {
		t.Fatal(err)
	}
	if !request.RequiresPreview || request.PreviewCommand.Hash == "" || request.ExecuteCommand != (executionpostgres.PayloadPointer{}) {
		t.Fatalf("preview request=%#v", request)
	}
	command := loadToolCommand(t, payloads, execution.Claim.TenantID, planned.CommandID, "tool-preview-command", request.PreviewCommand)
	if command.ToolCallID != planned.ToolCallID {
		t.Fatalf("preview command=%#v", command)
	}
	regular := workerPlanCall(false, false)
	regular.RequestHash = strings.Repeat("e", 64)
	if _, err = builder.BuildTools(t.Context(), execution, turn, MessageDocument{}, []ValidatedToolCall{preview, regular}); err == nil {
		t.Fatal("mixed preview/execution group was accepted")
	}
}

func TestProductionResultPlanBuilderFreezesDirectApproval(t *testing.T) {
	payloads := &memoryPayloads{}
	builder := productionPlanBuilder(payloads)
	execution, turn := planExecution()
	call := workerPlanCall(true, false)
	plan, err := builder.BuildApprovals(t.Context(), execution, turn, MessageDocument{}, []ValidatedToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PermissionSnapshot != call.Tool.PermissionSnapshot || !plan.ApprovalExpiresAt.Equal(builder.now().Add(builder.ApprovalTTL)) || len(plan.ToolRequests) != 1 || plan.RunWaitingApprovalEvent.Hash == "" {
		t.Fatalf("plan=%#v", plan)
	}
	request := plan.ToolRequests[0]
	recomputed, err := executionpostgres.DirectToolProposalHash(request, plan.PermissionSnapshot)
	if err != nil || recomputed != request.ProposalHash || len(request.ProposalHash) != 64 || request.ScopeSnapshot.Hash == "" || request.NotifyApproval.Hash == "" {
		t.Fatalf("request=%#v recomputed=%s err=%v", request, recomputed, err)
	}
	planned, err := executionpostgres.DirectApprovalPlanningIDs(builder.IDKey, execution.Claim.RunID, execution.Claim.RunVersion, plan.StepID, 0, call.RequestHash)
	if err != nil {
		t.Fatal(err)
	}
	command := loadToolCommand(t, payloads, execution.Claim.TenantID, planned.ExecuteCommandID, "tool-execute-command", request.ExecuteCommand)
	if command.ToolCallID != planned.ToolCallID || command.Input.Ref != request.NormalizedInputRef || command.DescriptorHash != call.Tool.DescriptorHash {
		t.Fatalf("command=%#v", command)
	}
	if _, err = payloads.Get(t.Context(), payload.Descriptor{TenantID: execution.Claim.TenantID, ObjectID: planned.NotifyCommandID, Class: "approval-notification-command", ContentType: "application/json"}, payload.Manifest{Ref: request.NotifyApproval.Ref, Hash: request.NotifyApproval.Hash}); err != nil {
		t.Fatalf("notification command AAD is not bound to command id: %v", err)
	}
}

func TestProductionResultPlanBuilderCreatesChildRunStartCommand(t *testing.T) {
	payloads := &memoryPayloads{}
	builder := productionPlanBuilder(payloads)
	execution, turn := planExecution()
	call := childPlanCall()
	plan, err := builder.BuildChildren(t.Context(), execution, turn, MessageDocument{}, []ValidatedToolCall{call})
	if err != nil {
		t.Fatal(err)
	}
	if plan.JoinPolicy != "all" || plan.QuorumCount != 1 || len(plan.Children) != 1 || plan.ParentWaitingEvent.Hash == "" {
		t.Fatalf("plan=%#v", plan)
	}
	child := plan.Children[0]
	if child.BehaviorProfile != "coach" || child.BehaviorEnvironment != "production" || child.BudgetMicrounits != 1000 || !child.Required || !child.DueAt.Equal(builder.now().Add(300*time.Second)) {
		t.Fatalf("child=%#v", child)
	}
	startID, err := executionpostgres.RunStartCommandID(builder.IDKey, child.RunID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := payloads.Get(t.Context(), payload.Descriptor{TenantID: execution.Claim.TenantID, ObjectID: startID, Class: "agent-run-command", ContentType: "application/json"}, payload.Manifest{Ref: child.StartCommand.Ref, Hash: child.StartCommand.Hash})
	if err != nil {
		t.Fatal(err)
	}
	var command CommandPayload
	if json.Unmarshal(encoded, &command) != nil || command.RunID != child.RunID || command.CorrelationID != execution.Payload.CorrelationID {
		t.Fatalf("start command=%s", encoded)
	}
}

func productionPlanBuilder(payloads payload.Store) ProductionResultPlanBuilder {
	now := time.Date(2026, time.July, 16, 5, 0, 0, 0, time.UTC)
	return ProductionResultPlanBuilder{
		Payloads: payloads, IDKey: []byte("0123456789abcdef0123456789abcdef"), ApprovalTTL: 20 * time.Minute,
		Notification: PlanSchedule{QueueClass: "interactive", ResourceClass: "notification", Priority: 60, CostUnits: 1, MaxAttempts: 8},
		Now:          func() time.Time { return now },
	}
}

func planExecution() (Execution, TurnResult) {
	execution := Execution{Payload: CommandPayload{SchemaVersion: 1, RunID: "run-1", CorrelationID: "correlation-1"}, Claim: executionpostgres.RunClaim{RunID: "run-1", TenantID: "tenant-1", UserID: "user-1", RunVersion: 4, AttemptID: "attempt-1"}}
	turn := TurnResult{Plan: TurnPlan{LLMAttemptID: "llm-attempt-1"}, SelectedAttemptID: "provider-attempt-1"}
	turn.Provider.ResponseHash = strings.Repeat("9", 64)
	return execution, turn
}

func workerPlanCall(manual, preview bool) ValidatedToolCall {
	return ValidatedToolCall{
		ProviderCallID: "provider-call-1", Input: json.RawMessage(`{"query":"status"}`), RequestHash: strings.Repeat("d", 64),
		Tool: ResolvedTool{
			Name: "provider_lookup", DescriptorSnapshotID: "provider_lookup@1.2.3", DescriptorHash: strings.Repeat("a", 64),
			ExecutionKind: ToolExecutionWorker, ManualApprovalRequired: manual, RequiresPreview: preview,
			EffectClass: "read_only", QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2, MaxAttempts: 5,
			PolicySnapshotID: "policy-1", PolicyHash: strings.Repeat("b", 64), PolicyVersion: 7, PermissionSnapshot: "membership:m1:v3:role:member",
		},
	}
}

func childPlanCall() ValidatedToolCall {
	return ValidatedToolCall{
		ProviderCallID: "provider-child-1", RequestHash: strings.Repeat("c", 64),
		Input: json.RawMessage(`{"profile":"coach","environment":"production","budget":{"max_cost_microunits":1000},"budget_microunits":1000,"timeout_seconds":300,"required":true}`),
		Tool: ResolvedTool{
			Name: "spawn_agent_run", DescriptorSnapshotID: "spawn_agent_run@1.0.0", DescriptorHash: strings.Repeat("8", 64),
			ExecutionKind: ToolExecutionChild, EffectClass: "read_only", QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5,
			PolicySnapshotID: "policy-1", PolicyHash: strings.Repeat("b", 64), PolicyVersion: 7, PermissionSnapshot: "membership:m1:v3:role:member",
		},
	}
}

func loadToolCommand(t *testing.T, store payload.Store, tenantID, commandID, class string, pointer executionpostgres.PayloadPointer) toolworker.CommandPayload {
	t.Helper()
	encoded, err := store.Get(t.Context(), payload.Descriptor{TenantID: tenantID, ObjectID: commandID, Class: class, ContentType: "application/json"}, payload.Manifest{Ref: pointer.Ref, Hash: pointer.Hash})
	if err != nil {
		t.Fatal(err)
	}
	var command toolworker.CommandPayload
	if json.Unmarshal(encoded, &command) != nil {
		t.Fatalf("invalid command: %s", encoded)
	}
	return command
}
