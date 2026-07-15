package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	"github.com/langshift/lites/internal/llmgateway"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

type turnBuilderStub struct{ plan TurnPlan }

func (builder turnBuilderStub) BuildTurn(context.Context, Execution) (TurnPlan, error) {
	return builder.plan, nil
}

type attemptStoreStub struct {
	started   []llmpostgres.StartLLMAttemptCommand
	prepared  []llmpostgres.PrepareProviderAttemptCommand
	finalized []llmpostgres.FinalizeLLMAttemptCommand
	tokens    int
}

func (store *attemptStoreStub) StartLLMAttempt(_ context.Context, command llmpostgres.StartLLMAttemptCommand) (llmpostgres.LLMAttempt, error) {
	store.started = append(store.started, command)
	return llmpostgres.LLMAttempt{AttemptID: command.AttemptID, ContextManifestHash: "manifest-hash", Status: "running", Version: 1}, nil
}
func (store *attemptStoreStub) IssuePrepareToken() (string, error) {
	store.tokens++
	return "prepare-token-" + string(rune('0'+store.tokens)), nil
}
func (store *attemptStoreStub) IssueCompletionToken() (string, error) {
	store.tokens++
	return "completion-token-" + string(rune('0'+store.tokens)), nil
}
func (store *attemptStoreStub) PrepareProviderAttempt(_ context.Context, command llmpostgres.PrepareProviderAttemptCommand) (llmpostgres.PreparedProviderAttempt, error) {
	store.prepared = append(store.prepared, command)
	return llmpostgres.PreparedProviderAttempt{AttemptID: command.AttemptID, Status: "prepared", Ordinal: command.Ordinal}, nil
}
func (store *attemptStoreStub) FinalizeLLMAttempt(_ context.Context, command llmpostgres.FinalizeLLMAttemptCommand) (llmpostgres.FinalizedLLMAttempt, error) {
	store.finalized = append(store.finalized, command)
	return llmpostgres.FinalizedLLMAttempt{AttemptID: command.AttemptID, Status: command.Status, SelectedProviderAttemptID: command.SelectedProviderAttemptID}, nil
}

type billingStub struct {
	commands []billingpostgres.ReserveCommand
}

func (store *billingStub) Reserve(_ context.Context, command billingpostgres.ReserveCommand) (billingpostgres.Reservation, error) {
	store.commands = append(store.commands, command)
	return billingpostgres.Reservation{ReservationID: command.ReservationID, Status: "reserved"}, nil
}

type gatewayOutcome struct {
	execution llmgateway.Execution
	err       error
}
type gatewayStub struct {
	outcomes []gatewayOutcome
	commands []llmgateway.ExecuteCommand
}

func (gateway *gatewayStub) PrepareRequest(candidate llmpostgres.ModelCandidate, _ provider.Request) (string, error) {
	return "request-hash-" + candidate.ModelID, nil
}
func (gateway *gatewayStub) Execute(_ context.Context, command llmgateway.ExecuteCommand) (llmgateway.Execution, error) {
	gateway.commands = append(gateway.commands, command)
	outcome := gateway.outcomes[len(gateway.commands)-1]
	return outcome.execution, outcome.err
}

func TestLLMTurnExecutorFallsBackWithSeparateDurableAttempts(t *testing.T) {
	plan := validTurnPlanFixture()
	plan.Candidates[0].FallbackOn = []string{provider.ClassRateLimited}
	attempts := &attemptStoreStub{}
	billing := &billingStub{}
	gateway := &gatewayStub{outcomes: []gatewayOutcome{
		{execution: llmgateway.Execution{Durable: true}, err: &provider.CallError{Class: provider.ClassRateLimited}},
		{execution: llmgateway.Execution{Durable: true, ProviderResult: provider.Result{ResponseHash: "response-2", ProviderRequestID: "provider-request-2", Text: "done"}}},
	}}
	executor := validTurnExecutor(plan, attempts, billing, gateway)
	result, err := executor.Execute(t.Context(), executionFixture())
	if err != nil {
		t.Fatal(err)
	}
	if result.SelectedAttemptID != "provider-row-2" || result.Provider.Text != "done" || len(attempts.prepared) != 2 || len(billing.commands) != 2 || len(gateway.commands) != 2 || len(attempts.finalized) != 1 {
		t.Fatalf("result=%#v prepared=%d reserved=%d dispatched=%d finalized=%d", result, len(attempts.prepared), len(billing.commands), len(gateway.commands), len(attempts.finalized))
	}
	if attempts.prepared[0].FallbackFromID != "" || attempts.prepared[1].FallbackFromID != "provider-row-1" || attempts.prepared[1].Ordinal != 2 {
		t.Fatalf("fallback chain=%#v", attempts.prepared)
	}
	if attempts.finalized[0].Status != "completed" || attempts.finalized[0].SelectedProviderAttemptID != "provider-row-2" || attempts.finalized[0].ResultHash != "response-2" {
		t.Fatalf("finalized=%#v", attempts.finalized[0])
	}
	if gateway.commands[0].Dispatch.RequestHash != "request-hash-model-1" || gateway.commands[0].Dispatch.CompletionToken == gateway.commands[1].Dispatch.CompletionToken {
		t.Fatalf("dispatches=%#v", gateway.commands)
	}
}

func TestLLMTurnExecutorStopsFallbackOutsideFrozenPolicy(t *testing.T) {
	plan := validTurnPlanFixture()
	attempts := &attemptStoreStub{}
	gateway := &gatewayStub{outcomes: []gatewayOutcome{{execution: llmgateway.Execution{Durable: true}, err: &provider.CallError{Class: provider.ClassContentFiltered}}}}
	result, err := validTurnExecutor(plan, attempts, &billingStub{}, gateway).Execute(t.Context(), executionFixture())
	var classified *provider.CallError
	if !errors.As(err, &classified) || classified.Class != provider.ClassContentFiltered {
		t.Fatalf("error=%v", err)
	}
	if len(gateway.commands) != 1 || len(attempts.finalized) != 1 || attempts.finalized[0].Status != "failed" || attempts.finalized[0].FailureCode != provider.ClassContentFiltered || result.Finalized.Status != "failed" {
		t.Fatalf("commands=%d finalized=%#v result=%#v", len(gateway.commands), attempts.finalized, result)
	}
}

func TestLLMTurnExecutorLeavesUnknownOutcomeForReconciliation(t *testing.T) {
	plan := validTurnPlanFixture()
	attempts := &attemptStoreStub{}
	unknown := &provider.CallError{Class: provider.ClassTimeout, OutcomeUnknown: true}
	gateway := &gatewayStub{outcomes: []gatewayOutcome{{execution: llmgateway.Execution{Durable: true}, err: unknown}}}
	_, err := validTurnExecutor(plan, attempts, &billingStub{}, gateway).Execute(t.Context(), executionFixture())
	if !errors.Is(err, ErrProviderOutcomeUnknown) || len(attempts.finalized) != 0 {
		t.Fatalf("error=%v finalized=%#v", err, attempts.finalized)
	}
}

func TestLLMTurnExecutorFailsClosedWhenProviderResultIsNotDurable(t *testing.T) {
	plan := validTurnPlanFixture()
	attempts := &attemptStoreStub{}
	gateway := &gatewayStub{outcomes: []gatewayOutcome{{execution: llmgateway.Execution{ProviderResult: provider.Result{Text: "must-not-escape"}}}}}
	_, err := validTurnExecutor(plan, attempts, &billingStub{}, gateway).Execute(t.Context(), executionFixture())
	if !errors.Is(err, llmgateway.ErrResultNotRecorded) || len(attempts.finalized) != 0 {
		t.Fatalf("error=%v finalized=%#v", err, attempts.finalized)
	}
}

func TestLLMTurnExecutorRejectsCandidateManifestDrift(t *testing.T) {
	plan := validTurnPlanFixture()
	plan.Candidates[0].Candidate.ModelVersion = "substituted"
	gateway := &gatewayStub{}
	_, err := validTurnExecutor(plan, &attemptStoreStub{}, &billingStub{}, gateway).Execute(t.Context(), executionFixture())
	if !errors.Is(err, ErrTurnPlan) || len(gateway.commands) != 0 {
		t.Fatalf("error=%v", err)
	}
}

func validTurnExecutor(plan TurnPlan, attempts *attemptStoreStub, billing *billingStub, gateway *gatewayStub) LLMTurnExecutor {
	return LLMTurnExecutor{
		Builder: turnBuilderStub{plan}, Attempts: attempts, Billing: billing, Gateway: gateway, Payloads: &memoryPayloads{},
		Actor: json.RawMessage(`{"kind":"service","name":"agent-worker"}`), PrepareTTL: time.Minute,
		CompletionTTL: 2 * time.Minute, ReconciliationDelay: time.Hour,
		Now: func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) },
	}
}

func validTurnPlanFixture() TurnPlan {
	models := []llmpostgres.ModelCandidate{
		{ProviderID: "openai", ModelID: "model-1", ModelVersion: "2026-07-01", BoundHost: "api.openai.com", PricingVersion: "pricing-1"},
		{ProviderID: "anthropic", ModelID: "model-2", ModelVersion: "2026-06-15", BoundHost: "api.anthropic.com", PricingVersion: "pricing-2"},
	}
	manifest := llmpostgres.ContextManifest{SchemaVersion: 1, Run: llmpostgres.SnapshotBinding{ID: "run-1", Version: 3, Hash: "run-hash"}, Prompt: llmpostgres.SnapshotBinding{ID: "prompt-1", Version: 1, Hash: "prompt-hash"}, Router: llmpostgres.SnapshotBinding{ID: "router-1", Version: 1, Hash: "router-hash"}, Budget: llmpostgres.SnapshotBinding{ID: "budget-1", Version: 1, Hash: "budget-hash"}, Policy: llmpostgres.SnapshotBinding{ID: "policy-1", Version: 1, Hash: "policy-hash"}, CandidateModels: models}
	return TurnPlan{
		LLMAttemptID: "llm-1", AttemptKey: "turn:1", StreamGeneration: 1, Manifest: manifest,
		Request:              provider.Request{Messages: []provider.Message{{Role: "user", Content: []provider.ContentBlock{{Type: "text", Text: "hello"}}}}, MaxOutputTokens: 100},
		EstimatedInputTokens: 10, FailureEstimate: llmgateway.FailureEstimate{InputTokens: 10},
		Candidates: []ProviderPlan{
			{AttemptID: "provider-row-1", ProviderAttemptID: "provider-wire-1", ReservationID: "reservation-1", ReservationRequestID: "reservation-request-1", BucketID: "bucket-1", ReservedUnits: 100, Candidate: models[0]},
			{AttemptID: "provider-row-2", ProviderAttemptID: "provider-wire-2", ReservationID: "reservation-2", ReservationRequestID: "reservation-request-2", BucketID: "bucket-1", ReservedUnits: 100, Candidate: models[1]},
		},
	}
}

func executionFixture() Execution {
	return Execution{Command: deliveredCommand(), Payload: CommandPayload{SchemaVersion: 1, RunID: "run-1", CorrelationID: "correlation-1"}, Claim: validClaim(), TurnIndex: 1}
}
