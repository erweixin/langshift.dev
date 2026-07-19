package agentworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func (store *memoryPayloads) Get(_ context.Context, descriptor payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	prefix := descriptor.TenantID + "/" + descriptor.Class + "/" + descriptor.ObjectID + "/"
	if !strings.HasPrefix(manifest.Ref, prefix) {
		return nil, payload.ErrIntegrity
	}
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
	claimFunc     func(executionpostgres.ClaimRunCommand) (executionpostgres.RunClaim, error)
	claimCalls    int
}

func (store *runStoreStub) ClaimStart(_ context.Context, command executionpostgres.ClaimRunCommand) (executionpostgres.RunClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCommand = command
	store.claimCalls++
	if store.claimFunc != nil {
		return store.claimFunc(command)
	}
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

type agentMetricsStub struct {
	heartbeats        int
	active            int64
	replays           int
	completed         int
	executions        int
	executionOutcomes []string
}

func (metrics *agentMetricsStub) AddLeaseHeartbeats(_ context.Context, count int64, leaseKind string) {
	if leaseKind == "run" {
		metrics.heartbeats += int(count)
	}
}

func (metrics *agentMetricsStub) AddExecutingRuns(_ context.Context, delta int64, _ string) {
	metrics.active += delta
}

func (metrics *agentMetricsStub) AddRunReplays(_ context.Context, count int64, _ string) {
	metrics.replays += int(count)
}

func (metrics *agentMetricsStub) AddRunDeadlineTransition(_ context.Context, _ string, _ bool, _ string) {
	metrics.completed++
}

func (metrics *agentMetricsStub) AddRunExecution(_ context.Context, _, outcome string) {
	metrics.executions++
	metrics.executionOutcomes = append(metrics.executionOutcomes, outcome)
}

func (*agentMetricsStub) AddActiveProviderRequests(context.Context, int64, string) {}
func (*agentMetricsStub) AddProviderTokens(context.Context, int64, string, string) {}
func (*agentMetricsStub) ObserveFirstSafeToken(context.Context, time.Duration, string) {
}

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
	metrics := &agentMetricsStub{}
	handler.Metrics = metrics
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	message, err := payloads.Get(t.Context(), payload.Descriptor{TenantID: claim.TenantID, ObjectID: "00000000-0000-4000-8000-000000000111", Class: "run-message", ContentType: "application/json"}, payload.Manifest{Ref: runs.message.Message.Ref, Hash: runs.message.Message.Hash})
	if err != nil || string(message) != `{"schema_version":1,"role":"assistant","content":[{"type":"text","text":"done"}]}` {
		t.Fatalf("canonical assistant message=%s err=%v", message, err)
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if runs.heartbeats < 1 || metrics.heartbeats != runs.heartbeats || metrics.active != 0 || metrics.completed != 1 || metrics.executions != 1 {
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
	metrics := &agentMetricsStub{}
	handler.Metrics = metrics
	err := handler.Handle(t.Context(), delivered)
	if !errors.Is(err, ErrHeartbeatLost) {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe lease-loss cancellation")
	}
	if metrics.active != 0 || metrics.executions != 1 || len(metrics.executionOutcomes) != 1 || metrics.executionOutcomes[0] != "failed" {
		t.Fatalf("metrics=%#v", metrics)
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

func TestHandlerStartResumeCrashRedeliveryConverges100(t *testing.T) {
	for repetition := 0; repetition < 100; repetition++ {
		payloads := &memoryPayloads{}
		start := deliveredCommand()
		start.CommandID = fmt.Sprintf("10000000-0000-4000-8000-%012d", repetition+1000)
		resume := start
		resume.CommandID = fmt.Sprintf("20000000-0000-4000-8000-%012d", repetition+1000)
		resume.CommandType = "ResumeAgentRun"
		resume.QueueGeneration = 2
		resume.DispatchVersion = 2
		putCommand(t, payloads, &start, CommandPayload{SchemaVersion: 1, RunID: start.AggregateID, CorrelationID: "correlation-redelivery"})
		putCommand(t, payloads, &resume, CommandPayload{SchemaVersion: 1, RunID: resume.AggregateID, CorrelationID: "correlation-redelivery"})

		startClaims, resumeClaims := 0, 0
		first := validClaim()
		replacement := first
		replacement.AttemptID = fmt.Sprintf("30000000-0000-4000-8000-%012d", repetition+1000)
		replacement.Fence++
		replacement.LeaseToken = "replacement-token"
		resumed := replacement
		resumed.AttemptID = fmt.Sprintf("40000000-0000-4000-8000-%012d", repetition+1000)
		resumed.Fence++
		resumed.RunVersion++
		resumed.LeaseToken = "resume-token"
		runs := &runStoreStub{}
		runs.claimFunc = func(command executionpostgres.ClaimRunCommand) (executionpostgres.RunClaim, error) {
			switch command.Command.CommandType {
			case "StartAgentRun":
				startClaims++
				switch startClaims {
				case 1:
					return first, nil
				case 2:
					return executionpostgres.RunClaim{}, executionpostgres.ErrClaimBusy
				case 3:
					return replacement, nil
				default:
					return executionpostgres.RunClaim{}, executionpostgres.ErrClaimCompleted
				}
			case "ResumeAgentRun":
				resumeClaims++
				if resumeClaims == 1 {
					return resumed, nil
				}
				return executionpostgres.RunClaim{}, executionpostgres.ErrClaimCompleted
			default:
				return executionpostgres.RunClaim{}, executionpostgres.ErrClaimConflict
			}
		}
		runnerCalls := 0
		handler := validHandler(payloads, runs, runnerFunc(func(_ context.Context, execution Execution) (Outcome, error) {
			runnerCalls++
			if execution.Command.CommandType == "StartAgentRun" && execution.Claim.Fence == first.Fence {
				return Outcome{}, errors.New("injected process crash")
			}
			if execution.Command.CommandType == "StartAgentRun" {
				outcome := successfulOutcome("")
				outcome.State, outcome.ResultHash, outcome.RunEvent, outcome.AttemptEvent = statemachine.RunWaitingTool, "", nil, nil
				outcome.Tools = &executionpostgres.RequestToolsCommand{PlanResultHash: "provider-result"}
				return outcome, nil
			}
			return successfulOutcome("resume-result"), nil
		}))
		if err := handler.Handle(t.Context(), start); err == nil || errors.Is(err, eventpostgres.ErrDeliveryBusy) {
			t.Fatalf("repetition %d injected crash error=%v", repetition, err)
		}
		if err := handler.Handle(t.Context(), start); !errors.Is(err, eventpostgres.ErrDeliveryBusy) {
			t.Fatalf("repetition %d live lease redelivery error=%v", repetition, err)
		}
		if err := handler.Handle(t.Context(), start); err != nil {
			t.Fatalf("repetition %d replacement claim error=%v", repetition, err)
		}
		if err := handler.Handle(t.Context(), resume); err != nil {
			t.Fatalf("repetition %d resume error=%v", repetition, err)
		}
		if err := handler.Handle(t.Context(), resume); err != nil {
			t.Fatalf("repetition %d completed duplicate error=%v", repetition, err)
		}
		if startClaims != 3 || resumeClaims != 2 || runnerCalls != 3 || runs.tools.Claim.Fence != replacement.Fence || runs.complete.Claim.Fence != resumed.Fence {
			t.Fatalf("repetition %d claims=%d/%d runner=%d tool_fence=%d terminal_fence=%d", repetition, startClaims, resumeClaims, runnerCalls, runs.tools.Claim.Fence, runs.complete.Claim.Fence)
		}
	}
	t.Log(`agent_handler_recovery={"repetitions":100,"duplicate_terminal_executions":0,"stale_fence_commits":0}`)
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
	now := time.Now()
	return executionpostgres.RunClaim{RunID: "run-1", TenantID: "tenant-1", UserID: "user-1", StoreEpoch: "epoch-1", RunVersion: 3, CommandID: "command-1", ConsumerName: "agent-worker", RequestHash: "request-hash", JobID: "job-1", InboxID: "inbox-1", AttemptID: "attempt-1", Fence: 1, LeaseToken: "lease-token", LeaseExpiresAt: now.Add(time.Minute), DueAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Second), QueueClass: "interactive"}
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
