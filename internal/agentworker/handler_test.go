package agentworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/payload"
)

type memoryPayloads struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (store *memoryPayloads) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.objects == nil {
		store.objects = map[string][]byte{}
	}
	digest := sha256.Sum256(value)
	hash := hex.EncodeToString(digest[:])
	ref := descriptor.TenantID + "/" + descriptor.Class + "/" + descriptor.ObjectID + "/" + hash
	store.objects[ref] = append([]byte(nil), value...)
	return payload.Manifest{Ref: ref, Hash: hash}, nil
}

func (store *memoryPayloads) Get(_ context.Context, _ payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.objects[manifest.Ref]
	if !ok {
		return nil, errors.New("missing payload")
	}
	digest := sha256.Sum256(value)
	if hex.EncodeToString(digest[:]) != manifest.Hash {
		return nil, payload.ErrIntegrity
	}
	return append([]byte(nil), value...), nil
}

type runStoreStub struct {
	mu            sync.Mutex
	claim         executionpostgres.RunClaim
	claimErr      error
	heartbeatErr  error
	heartbeats    int
	claimCommand  executionpostgres.ClaimRunCommand
	complete      executionpostgres.CompleteRunCommand
	message       executionpostgres.CompleteRunMessageCommand
	tools         executionpostgres.RequestToolsCommand
	approvals     executionpostgres.ProposeDirectToolsCommand
	children      executionpostgres.SpawnChildRunsCommand
	completionErr error
}

func (store *runStoreStub) ClaimStart(_ context.Context, command executionpostgres.ClaimRunCommand) (executionpostgres.RunClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCommand = command
	return store.claim, store.claimErr
}

func (store *runStoreStub) HeartbeatRun(_ context.Context, claim executionpostgres.RunClaim) (executionpostgres.RunClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.heartbeats++
	if store.heartbeatErr != nil {
		return executionpostgres.RunClaim{}, store.heartbeatErr
	}
	claim.LeaseExpiresAt = claim.LeaseExpiresAt.Add(time.Minute)
	return claim, nil
}

func (store *runStoreStub) CompleteRunTerminal(_ context.Context, command executionpostgres.CompleteRunCommand) (executionpostgres.CompletedRun, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.complete = command
	return executionpostgres.CompletedRun{}, store.completionErr
}

func (store *runStoreStub) CompleteRunWithMessage(_ context.Context, command executionpostgres.CompleteRunMessageCommand) (executionpostgres.CompletedRunMessage, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.message = command
	store.complete = command.Completion
	return executionpostgres.CompletedRunMessage{}, store.completionErr
}

func (store *runStoreStub) RequestTools(_ context.Context, command executionpostgres.RequestToolsCommand) (executionpostgres.ToolsRequested, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.tools = command
	return executionpostgres.ToolsRequested{}, store.completionErr
}

func (store *runStoreStub) ProposeDirectTools(_ context.Context, command executionpostgres.ProposeDirectToolsCommand) (executionpostgres.DirectToolsProposed, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.approvals = command
	return executionpostgres.DirectToolsProposed{}, store.completionErr
}

func (store *runStoreStub) SpawnChildRuns(_ context.Context, command executionpostgres.SpawnChildRunsCommand) (executionpostgres.ChildRunsSpawned, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.children = command
	return executionpostgres.ChildRunsSpawned{}, store.completionErr
}

type runnerFunc func(context.Context, Execution) (Outcome, error)

func (function runnerFunc) Execute(ctx context.Context, execution Execution) (Outcome, error) {
	return function(ctx, execution)
}

func TestHandlerClaimsHeartbeatsAndCompletesRootRun(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered := deliveredCommand()
	putCommand(t, payloads, &delivered, CommandPayload{SchemaVersion: 1, RunID: delivered.AggregateID, CorrelationID: "correlation-1"})
	claim := validClaim()
	runs := &runStoreStub{claim: claim}
	handler := validHandler(payloads, runs, runnerFunc(func(ctx context.Context, execution Execution) (Outcome, error) {
		if execution.Claim.AttemptID != claim.AttemptID || execution.Payload.CorrelationID != "correlation-1" {
			t.Fatal("runner received an unbound execution")
		}
		select {
		case <-time.After(18 * time.Millisecond):
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		return successfulOutcome("result-hash"), nil
	}))
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if runs.heartbeats < 1 {
		t.Fatal("long execution was not heartbeated")
	}
	if runs.claimCommand.Command.DispatchVersion != delivered.DispatchVersion || runs.claimCommand.RunEvent.Hash == "" || runs.claimCommand.AttemptExpiredEvent.Hash == "" {
		t.Fatalf("claim was not durably bound: %#v", runs.claimCommand)
	}
	if runs.complete.TargetState != statemachine.RunSucceeded || runs.complete.ResultHash != "result-hash" || !runs.complete.Claim.LeaseExpiresAt.After(claim.LeaseExpiresAt) || runs.complete.RunEvent.Hash == "" || runs.complete.AttemptCompletedEvent.Hash == "" {
		t.Fatalf("completion was not fenced: %#v", runs.complete)
	}
}

func TestHandlerCancelsRunnerWhenHeartbeatIsLost(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered := deliveredCommand()
	putCommand(t, payloads, &delivered, CommandPayload{SchemaVersion: 1, RunID: delivered.AggregateID, CorrelationID: "correlation-1"})
	runs := &runStoreStub{claim: validClaim(), heartbeatErr: executionpostgres.ErrExecutionRightConflict}
	cancelled := make(chan struct{})
	handler := validHandler(payloads, runs, runnerFunc(func(ctx context.Context, _ Execution) (Outcome, error) {
		<-ctx.Done()
		close(cancelled)
		return Outcome{}, ctx.Err()
	}))
	err := handler.Handle(t.Context(), delivered)
	if !errors.Is(err, ErrHeartbeatLost) {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe lease-loss cancellation")
	}
}

func TestHandlerCommitsStepPlanWithLatestHeartbeatClaim(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered := deliveredCommand()
	putCommand(t, payloads, &delivered, CommandPayload{SchemaVersion: 1, RunID: delivered.AggregateID, CorrelationID: "correlation-1"})
	claim := validClaim()
	runs := &runStoreStub{claim: claim}
	handler := validHandler(payloads, runs, runnerFunc(func(ctx context.Context, _ Execution) (Outcome, error) {
		select {
		case <-time.After(18 * time.Millisecond):
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		outcome := successfulOutcome("")
		outcome.State, outcome.ResultHash, outcome.RunEvent, outcome.AttemptEvent = statemachine.RunWaitingTool, "", nil, nil
		outcome.Tools = &executionpostgres.RequestToolsCommand{PlanResultHash: "provider-result"}
		return outcome, nil
	}))
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if runs.heartbeats < 1 || !runs.tools.Claim.LeaseExpiresAt.After(claim.LeaseExpiresAt) || runs.tools.ExpectedRunVersion != claim.RunVersion || string(runs.tools.Actor) != string(handler.Actor) || runs.tools.CorrelationID != "correlation-1" || runs.tools.AssistantMessage == nil || runs.tools.AssistantMessage.MessageID == "" || len(runs.tools.AssistantMessage.ContentHash) != 64 {
		t.Fatalf("step plan was not rebound to the live claim: heartbeats=%d plan=%#v", runs.heartbeats, runs.tools)
	}
	if runs.complete.Claim.RunID != "" {
		t.Fatal("waiting step was incorrectly committed as a terminal run")
	}
}

func TestHandlerCommitsDirectApprovalWithLatestHeartbeatClaim(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered := deliveredCommand()
	putCommand(t, payloads, &delivered, CommandPayload{SchemaVersion: 1, RunID: delivered.AggregateID, CorrelationID: "correlation-1"})
	claim := validClaim()
	runs := &runStoreStub{claim: claim}
	handler := validHandler(payloads, runs, runnerFunc(func(ctx context.Context, _ Execution) (Outcome, error) {
		select {
		case <-time.After(18 * time.Millisecond):
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		outcome := successfulOutcome("")
		outcome.State, outcome.ResultHash, outcome.RunEvent, outcome.AttemptEvent = statemachine.RunWaitingApproval, "", nil, nil
		outcome.Approvals = &executionpostgres.ProposeDirectToolsCommand{PlanResultHash: "provider-result"}
		return outcome, nil
	}))
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if runs.heartbeats < 1 || !runs.approvals.Claim.LeaseExpiresAt.After(claim.LeaseExpiresAt) || runs.approvals.ExpectedRunVersion != claim.RunVersion || string(runs.approvals.Actor) != string(handler.Actor) || runs.approvals.CorrelationID != "correlation-1" || runs.approvals.AssistantMessage == nil || runs.approvals.AssistantMessage.MessageID == "" || len(runs.approvals.AssistantMessage.ContentHash) != 64 {
		t.Fatalf("approval plan was not rebound to the live claim: heartbeats=%d plan=%#v", runs.heartbeats, runs.approvals)
	}
	if runs.complete.Claim.RunID != "" || runs.tools.Claim.RunID != "" {
		t.Fatal("waiting approval was committed through the wrong transaction")
	}
}

func TestHandlerRejectsPayloadSubstitutionBeforeClaim(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered := deliveredCommand()
	putCommand(t, payloads, &delivered, CommandPayload{SchemaVersion: 1, RunID: "different-run", CorrelationID: "correlation-1"})
	runs := &runStoreStub{claim: validClaim()}
	handler := validHandler(payloads, runs, runnerFunc(func(context.Context, Execution) (Outcome, error) {
		t.Fatal("runner must not see a substituted payload")
		return Outcome{}, nil
	}))
	err := handler.Handle(t.Context(), delivered)
	if !errors.Is(err, eventpostgres.ErrDeliveryConflict) || !errors.Is(err, ErrCommand) {
		t.Fatalf("error=%v", err)
	}
	if runs.claimCommand.Command.CommandID != "" {
		t.Fatal("invalid command reached ClaimStart")
	}
}

func TestHandlerMapsDurableClaimOutcomes(t *testing.T) {
	tests := []struct {
		name string
		from error
		to   error
	}{
		{"completed is acked", executionpostgres.ErrClaimCompleted, nil},
		{"live owner is retried", executionpostgres.ErrClaimBusy, eventpostgres.ErrDeliveryBusy},
		{"stale epoch is terminated", executionpostgres.ErrStaleEpoch, eventpostgres.ErrStaleStoreEpoch},
		{"conflict is terminated", executionpostgres.ErrClaimConflict, eventpostgres.ErrDeliveryConflict},
		{"not claimable is terminated", executionpostgres.ErrRunNotClaimable, eventpostgres.ErrDeliveryConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payloads := &memoryPayloads{}
			delivered := deliveredCommand()
			putCommand(t, payloads, &delivered, CommandPayload{SchemaVersion: 1, RunID: delivered.AggregateID, CorrelationID: "correlation-1"})
			runs := &runStoreStub{claimErr: test.from}
			handler := validHandler(payloads, runs, runnerFunc(func(context.Context, Execution) (Outcome, error) {
				t.Fatal("runner must not execute after claim outcome")
				return Outcome{}, nil
			}))
			err := handler.Handle(t.Context(), delivered)
			if test.to == nil && err != nil || test.to != nil && !errors.Is(err, test.to) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestHandlerBuildsCompleteChildJoinEvidence(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered := deliveredCommand()
	putCommand(t, payloads, &delivered, CommandPayload{SchemaVersion: 1, RunID: delivered.AggregateID, CorrelationID: "correlation-1"})
	runs := &runStoreStub{claim: validClaim()}
	child := &ChildOutcome{
		ResultSummary: map[string]any{"summary": "done"}, CompletedEvent: map[string]any{"state": "succeeded"},
		GroupJoinedEvent: map[string]any{"policy": "all"}, RunResumeQueuedEvent: map[string]any{"reason": "child_joined"},
		ResumeCommand: map[string]any{"schema_version": 1}, CancelRemainingCommand: map[string]any{"schema_version": 1},
		ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 4, ResumeMaxAttempts: 5,
	}
	handler := validHandler(payloads, runs, runnerFunc(func(context.Context, Execution) (Outcome, error) {
		outcome := successfulOutcome("child-result")
		outcome.RunEvent, outcome.Child = nil, child
		return outcome, nil
	}))
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	if runs.complete.Child == nil || len(runs.complete.Child.ResultSummary.Hash) != 64 || len(runs.complete.Child.CancelRemainingCommand.Hash) != 64 || runs.complete.RunEvent != (executionpostgres.PayloadPointer{}) {
		t.Fatalf("child completion=%#v", runs.complete)
	}
}

func successfulOutcome(resultHash string) Outcome {
	return Outcome{
		State: statemachine.RunSucceeded, ResultHash: resultHash,
		RunEvent: map[string]any{"answer_ref": "artifact://answer"}, AttemptEvent: map[string]any{"provider_attempts": 1},
		MessageID: "00000000-0000-4000-8000-000000000111",
		Message:   &MessageDocument{SchemaVersion: 1, Role: "assistant", Content: []provider.ContentBlock{{Type: "text", Text: "done"}}},
	}
}

func deliveredCommand() eventpostgres.DeliveredCommand {
	return eventpostgres.DeliveredCommand{TenantID: "tenant-1", StoreEpoch: "epoch-1", CommandID: "command-1", CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: "run-1", QueueGeneration: 2, DispatchVersion: 3}
}

func validClaim() executionpostgres.RunClaim {
	return executionpostgres.RunClaim{RunID: "run-1", TenantID: "tenant-1", UserID: "user-1", StoreEpoch: "epoch-1", RunVersion: 3, CommandID: "command-1", ConsumerName: "agent-worker", RequestHash: "request-hash", JobID: "job-1", InboxID: "inbox-1", AttemptID: "attempt-1", Fence: 1, LeaseToken: "lease-token", LeaseExpiresAt: time.Now().Add(time.Minute)}
}

func validHandler(payloads payload.Store, runs RunStore, runner Runner) Handler {
	return Handler{Payloads: payloads, Runs: runs, Runner: runner, ConsumerName: "agent-worker", WorkerID: "worker-1", Actor: json.RawMessage(`{"kind":"service","name":"agent-worker"}`), HeartbeatInterval: 5 * time.Millisecond}
}

func putCommand(t *testing.T, store payload.Store, delivered *eventpostgres.DeliveredCommand, command CommandPayload) {
	t.Helper()
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Put(t.Context(), payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.CommandID, Class: "agent-run-command", ContentType: "application/json"}, encoded)
	if err != nil {
		t.Fatal(err)
	}
	delivered.PayloadRef, delivered.PayloadHash = manifest.Ref, manifest.Hash
}
