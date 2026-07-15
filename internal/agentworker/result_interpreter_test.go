package agentworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

type resolverStub struct {
	tools map[string]ResolvedTool
	calls int
}

func (resolver *resolverStub) ResolveAndNormalize(_ context.Context, _ Execution, binding llmpostgres.SnapshotBinding, advertised provider.Tool, call provider.ToolCall) (ResolvedTool, json.RawMessage, string, error) {
	resolver.calls++
	tool, ok := resolver.tools[call.Name]
	if !ok || advertised.Name != call.Name || tool.DescriptorSnapshotID != binding.ID {
		return ResolvedTool{}, nil, "", errors.New("not resolved")
	}
	var value any
	if json.Unmarshal(call.Input, &value) != nil {
		return ResolvedTool{}, nil, "", errors.New("schema validation failed")
	}
	normalized, _ := json.Marshal(value)
	digest := sha256.Sum256(normalized)
	return tool, normalized, hex.EncodeToString(digest[:]), nil
}

type resultPlanStub struct {
	tools, children int
}

func (planner *resultPlanStub) BuildTools(_ context.Context, _ Execution, turn TurnResult, _ MessageDocument, calls []ValidatedToolCall) (executionpostgres.RequestToolsCommand, error) {
	planner.tools++
	plan := executionpostgres.RequestToolsCommand{PlanResultHash: turn.Provider.ResponseHash}
	for _, call := range calls {
		plan.ToolRequests = append(plan.ToolRequests, executionpostgres.ToolRequest{ToolName: call.Tool.Name, DescriptorSnapshotID: call.Tool.DescriptorSnapshotID, RequestHash: call.RequestHash, RequiresPreview: call.Tool.RequiresPreview})
	}
	return plan, nil
}

func (planner *resultPlanStub) BuildChildren(_ context.Context, _ Execution, turn TurnResult, _ MessageDocument, calls []ValidatedToolCall) (executionpostgres.SpawnChildRunsCommand, error) {
	planner.children++
	plan := executionpostgres.SpawnChildRunsCommand{PlanResultHash: turn.Provider.ResponseHash}
	for _, call := range calls {
		plan.Children = append(plan.Children, executionpostgres.ChildRunRequest{DescriptorSnapshotID: call.Tool.DescriptorSnapshotID, RequestHash: call.RequestHash})
	}
	return plan, nil
}

func TestResultInterpreterProducesBoundTerminalMessage(t *testing.T) {
	interpreter, execution := validResultInterpreter()
	turn := durableTurn()
	turn.Provider.Text = "A grounded answer."
	outcome, err := interpreter.Interpret(t.Context(), execution, turn)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != statemachine.RunSucceeded || outcome.ResultHash != turn.Provider.ResponseHash || outcome.MessageID == "" || outcome.Message == nil || outcome.Message.Role != "assistant" || outcome.Message.Content[0].Text != "A grounded answer." || outcome.Tools != nil || outcome.Children != nil {
		t.Fatalf("outcome=%#v", outcome)
	}
	second, err := interpreter.Interpret(t.Context(), execution, turn)
	if err != nil || second.MessageID != outcome.MessageID {
		t.Fatalf("message id is not deterministic: first=%q second=%q err=%v", outcome.MessageID, second.MessageID, err)
	}
}

func TestResultInterpreterBuildsApprovalPreviewToolPlan(t *testing.T) {
	interpreter, execution := validResultInterpreter()
	resolver := interpreter.Tools.(*resolverStub)
	resolver.tools["workspace_publish"] = ResolvedTool{Name: "workspace_publish", DescriptorSnapshotID: "workspace_publish@1", DescriptorHash: "hash-publish", ExecutionKind: ToolExecutionWorker, ManualApprovalRequired: true, RequiresPreview: true}
	turn := toolTurn([]provider.Tool{{Name: "workspace_publish"}}, []llmpostgres.SnapshotBinding{{ID: "workspace_publish@1", Version: 1, Hash: "hash-publish"}}, []provider.ToolCall{{ID: "call-1", Name: "workspace_publish", Input: json.RawMessage(`{"revision":"r1"}`)}})
	outcome, err := interpreter.Interpret(t.Context(), execution, turn)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != statemachine.RunWaitingTool || outcome.Tools == nil || len(outcome.Tools.ToolRequests) != 1 || !outcome.Tools.ToolRequests[0].RequiresPreview || outcome.Tools.PlanResultHash != turn.Provider.ResponseHash {
		t.Fatalf("outcome=%#v", outcome)
	}
}

func TestResultInterpreterBuildsChildPlan(t *testing.T) {
	interpreter, execution := validResultInterpreter()
	resolver := interpreter.Tools.(*resolverStub)
	resolver.tools["spawn_agent_run"] = ResolvedTool{Name: "spawn_agent_run", DescriptorSnapshotID: "spawn_agent_run@1", DescriptorHash: "hash-child", ExecutionKind: ToolExecutionChild}
	turn := toolTurn([]provider.Tool{{Name: "spawn_agent_run"}}, []llmpostgres.SnapshotBinding{{ID: "spawn_agent_run@1", Version: 1, Hash: "hash-child"}}, []provider.ToolCall{{ID: "call-child", Name: "spawn_agent_run", Input: json.RawMessage(`{"profile":"coach"}`)}})
	outcome, err := interpreter.Interpret(t.Context(), execution, turn)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != statemachine.RunWaitingChild || outcome.Children == nil || len(outcome.Children.Children) != 1 {
		t.Fatalf("outcome=%#v", outcome)
	}
}

func TestResultInterpreterRejectsMixedAtomicityDomains(t *testing.T) {
	interpreter, execution := validResultInterpreter()
	resolver := interpreter.Tools.(*resolverStub)
	resolver.tools["search"] = ResolvedTool{Name: "search", DescriptorSnapshotID: "search@1", DescriptorHash: "hash-search", ExecutionKind: ToolExecutionWorker}
	resolver.tools["spawn_agent_run"] = ResolvedTool{Name: "spawn_agent_run", DescriptorSnapshotID: "spawn_agent_run@1", DescriptorHash: "hash-child", ExecutionKind: ToolExecutionChild}
	turn := toolTurn(
		[]provider.Tool{{Name: "search"}, {Name: "spawn_agent_run"}},
		[]llmpostgres.SnapshotBinding{{ID: "search@1", Version: 1, Hash: "hash-search"}, {ID: "spawn_agent_run@1", Version: 1, Hash: "hash-child"}},
		[]provider.ToolCall{{ID: "call-search", Name: "search", Input: json.RawMessage(`{"q":"x"}`)}, {ID: "call-child", Name: "spawn_agent_run", Input: json.RawMessage(`{"profile":"coach"}`)}},
	)
	_, err := interpreter.Interpret(t.Context(), execution, turn)
	if !errors.Is(err, ErrResultRepair) {
		t.Fatalf("error=%v", err)
	}
	plans := interpreter.Plans.(*resultPlanStub)
	if plans.tools != 0 || plans.children != 0 {
		t.Fatal("mixed plan reached a commit planner")
	}
}

func TestResultInterpreterRejectsManifestAndFinalizationDrift(t *testing.T) {
	interpreter, execution := validResultInterpreter()
	turn := toolTurn([]provider.Tool{{Name: "search"}}, nil, []provider.ToolCall{{ID: "call", Name: "search", Input: json.RawMessage(`{}`)}})
	if _, err := interpreter.Interpret(t.Context(), execution, turn); !errors.Is(err, ErrResultIntegrity) {
		t.Fatalf("manifest drift error=%v", err)
	}
	turn = durableTurn()
	turn.Finalized.SelectedProviderAttemptID = "substituted"
	if _, err := interpreter.Interpret(t.Context(), execution, turn); !errors.Is(err, ErrResultIntegrity) {
		t.Fatalf("finalization drift error=%v", err)
	}
}

func validResultInterpreter() (ResultInterpreter, Execution) {
	resolver := &resolverStub{tools: map[string]ResolvedTool{}}
	plans := &resultPlanStub{}
	execution := Execution{Payload: CommandPayload{SchemaVersion: 1, RunID: "run-1", CorrelationID: "correlation-1"}, Claim: validClaim(), TurnIndex: 1}
	return ResultInterpreter{Tools: resolver, Plans: plans, IDKey: []byte("01234567890123456789012345678901")}, execution
}

func durableTurn() TurnResult {
	return TurnResult{
		Plan: TurnPlan{LLMAttemptID: "llm-attempt-1"}, SelectedAttemptID: "provider-attempt-1",
		Finalized: llmpostgres.FinalizedLLMAttempt{AttemptID: "llm-attempt-1", Status: "completed", SelectedProviderAttemptID: "provider-attempt-1"},
		Provider:  provider.Result{ProviderRequestID: "provider-request-1", Model: "model-1", Status: "completed", FinishReason: "stop", InputTokens: 10, OutputTokens: 4, RequestHash: "request-hash", ResponseHash: "response-hash"},
	}
}

func toolTurn(tools []provider.Tool, bindings []llmpostgres.SnapshotBinding, calls []provider.ToolCall) TurnResult {
	turn := durableTurn()
	turn.Plan.Request.Tools, turn.Plan.Manifest.Tools = tools, bindings
	turn.Provider.FinishReason, turn.Provider.ToolCalls = "tool_use", calls
	return turn
}
