//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestRecursiveRunCancellationFaultInjection100(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "fb000000-0000-4000-8000-000000000001"
	const userID = "fb000000-0000-4000-8000-000000000002"
	const storeEpoch = "fb000000-0000-4000-8000-000000000003"
	start := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "recursive-cancellation-fault@example.invalid", start)
	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloadsStore := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "recursive-cancellation-fault-v1", Material: bytes.Repeat([]byte{0xfb}, 32)}}, Blobs: blobs}

	const repetitions = 100
	var propagationReplays, lateCommitsRejected int
	for iteration := 0; iteration < repetitions; iteration++ {
		clock := start.Add(time.Duration(iteration) * time.Minute)
		base := 20000 + iteration*100
		rootRunID := recursiveFaultUUID(base + 1)
		childRunID := recursiveFaultUUID(base + 2)
		grandchildRunID := recursiveFaultUUID(base + 3)
		conversationID := recursiveFaultUUID(base + 4)
		correlationID := recursiveFaultUUID(base + 5)
		rootCancellationID := recursiveFaultUUID(base + 6)
		store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }}, IDKey: bytes.Repeat([]byte{0xf6}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return clock }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "recursive-cancellation-fault", Pepper: bytes.Repeat([]byte{0xf7}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
		pointer := func(name string) PayloadPointer {
			return PayloadPointer{Ref: "encrypted://recursive-fault/" + rootRunID + "/" + name, Hash: "recursive-fault-" + name}
		}

		rootStart := pointer("root-start")
		accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: rootRunID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: clock.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":10000}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: pointer("root-accepted"), QueuedEvent: pointer("root-queued"), StartCommand: rootStart, QueueClass: "interactive", ResourceClass: "llm", Priority: 80, CostUnits: 1, MaxAttempts: 5})
		if err != nil {
			t.Fatalf("iteration %d accept root: %v", iteration, err)
		}
		rootClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: rootRunID, PayloadRef: rootStart.Ref, PayloadHash: rootStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: fmt.Sprintf("root-%03d", iteration), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("root-started"), AttemptStartedEvent: pointer("root-attempt-started"), AttemptExpiredEvent: pointer("root-attempt-expired")})
		if err != nil {
			t.Fatalf("iteration %d claim root: %v", iteration, err)
		}
		childStart := pointer("child-start")
		rootSpawn, err := store.SpawnChildRuns(ctx, SpawnChildRunsCommand{Claim: rootClaim, ExpectedRunVersion: rootClaim.RunVersion, StepID: fmt.Sprintf("root-child-%03d", iteration), JoinPolicy: "all", QuorumCount: 1, Children: []ChildRunRequest{{RunID: childRunID, RequestHash: fmt.Sprintf("root-child-%03d", iteration), DescriptorSnapshotID: "spawn_agent_run@sha256:contract", NormalizedInputRef: pointer("child-input").Ref, BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":4000}`), BudgetMicrounits: 4000, DueAt: clock.Add(45 * time.Minute), Required: true, QueueClass: "interactive", ResourceClass: "llm", Priority: 70, CostUnits: 1, MaxAttempts: 5, ToolSucceededEvent: pointer("child-tool"), AcceptedEvent: pointer("child-accepted"), QueuedEvent: pointer("child-queued"), StartCommand: childStart}}, PlanResultHash: "root-child-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("root-attempt-completed"), ParentWaitingEvent: pointer("root-waiting")})
		if err != nil {
			t.Fatalf("iteration %d spawn child: %v", iteration, err)
		}
		childClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: rootSpawn.Children[0].StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: childRunID, PayloadRef: childStart.Ref, PayloadHash: childStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: fmt.Sprintf("child-%03d", iteration), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("child-started"), AttemptStartedEvent: pointer("child-attempt-started"), AttemptExpiredEvent: pointer("child-attempt-expired")})
		if err != nil {
			t.Fatalf("iteration %d claim child: %v", iteration, err)
		}
		grandchildStart := pointer("grandchild-start")
		childSpawn, err := store.SpawnChildRuns(ctx, SpawnChildRunsCommand{Claim: childClaim, ExpectedRunVersion: childClaim.RunVersion, StepID: fmt.Sprintf("child-grandchild-%03d", iteration), JoinPolicy: "all", QuorumCount: 1, Children: []ChildRunRequest{{RunID: grandchildRunID, RequestHash: fmt.Sprintf("grandchild-%03d", iteration), DescriptorSnapshotID: "spawn_agent_run@sha256:contract", NormalizedInputRef: pointer("grandchild-input").Ref, BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":1000}`), BudgetMicrounits: 1000, DueAt: clock.Add(30 * time.Minute), Required: true, QueueClass: "interactive", ResourceClass: "llm", Priority: 60, CostUnits: 1, MaxAttempts: 5, ToolSucceededEvent: pointer("grandchild-tool"), AcceptedEvent: pointer("grandchild-accepted"), QueuedEvent: pointer("grandchild-queued"), StartCommand: grandchildStart}}, PlanResultHash: "grandchild-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("child-attempt-completed"), ParentWaitingEvent: pointer("child-waiting")})
		if err != nil {
			t.Fatalf("iteration %d spawn grandchild: %v", iteration, err)
		}
		grandchildClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: childSpawn.Children[0].StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: grandchildRunID, PayloadRef: grandchildStart.Ref, PayloadHash: grandchildStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: fmt.Sprintf("grandchild-%03d", iteration), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("grandchild-started"), AttemptStartedEvent: pointer("grandchild-attempt-started"), AttemptExpiredEvent: pointer("grandchild-attempt-expired")})
		if err != nil {
			t.Fatalf("iteration %d claim grandchild: %v", iteration, err)
		}

		requested, err := store.RequestCancellation(ctx, cancellationTestCommand(rootCancellationID, tenantID, userID, rootRunID, correlationID, rootSpawn.ParentRunVersion, "a"))
		if err != nil || requested.Settled {
			t.Fatalf("iteration %d request cancellation=%#v error=%v", iteration, requested, err)
		}
		reconciler := RunCancellationReconcilerService{Store: store, Payloads: payloadsStore, IDKey: store.IDKey}
		clock = clock.Add(cancellationReconciliationDelay)
		rootPropagation, err := reconciler.ReconcileDueCancellation(ctx, tenantID, rootCancellationID, storeEpoch)
		if err != nil || rootPropagation.Settled || rootPropagation.CancellationVersion != 3 {
			t.Fatalf("iteration %d root propagation=%#v error=%v", iteration, rootPropagation, err)
		}
		if replay, replayErr := store.PropagateRunCancellation(ctx, PropagateRunCancellationCommand{CancellationID: rootCancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedCancellationVersion: 2, BatchSize: 10, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, Children: map[string]PropagatedRunCancellationPayloads{}}); replayErr != nil || !replay.Replayed || !replay.Complete {
			t.Fatalf("iteration %d root propagation replay=%#v error=%v", iteration, replay, replayErr)
		}
		propagationReplays++
		childCancellationID, err := store.propagatedCancellationID(rootCancellationID, childRunID)
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(cancellationReconciliationDelay)
		childPropagation, err := reconciler.ReconcileDueCancellation(ctx, tenantID, childCancellationID, storeEpoch)
		if err != nil || childPropagation.Settled || childPropagation.CancellationVersion != 3 {
			t.Fatalf("iteration %d child propagation=%#v error=%v", iteration, childPropagation, err)
		}
		if replay, replayErr := store.PropagateRunCancellation(ctx, PropagateRunCancellationCommand{CancellationID: childCancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedCancellationVersion: 2, BatchSize: 10, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, Children: map[string]PropagatedRunCancellationPayloads{}}); replayErr != nil || !replay.Replayed || !replay.Complete {
			t.Fatalf("iteration %d child propagation replay=%#v error=%v", iteration, replay, replayErr)
		}
		propagationReplays++
		grandchildCancellationID, err := store.propagatedCancellationID(rootCancellationID, grandchildRunID)
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(cancellationReconciliationDelay)
		for _, cancellationID := range []string{grandchildCancellationID, childCancellationID, rootCancellationID} {
			settled, settleErr := reconciler.ReconcileDueCancellation(ctx, tenantID, cancellationID, storeEpoch)
			if settleErr != nil || !settled.Settled {
				t.Fatalf("iteration %d settle %s=%#v error=%v", iteration, cancellationID, settled, settleErr)
			}
		}
		lateCompletion := ChildRunCompletion{ResultSummary: PayloadPointer{Ref: pointer("late-summary").Ref, Hash: fmt.Sprintf("%064x", iteration+1)}, CompletedEvent: pointer("late-completed"), GroupJoinedEvent: pointer("late-group"), RunResumeQueuedEvent: pointer("late-resume"), ResumeCommand: pointer("late-command"), CancelRemainingCommand: PayloadPointer{Ref: pointer("late-cancel").Ref, Hash: fmt.Sprintf("%064x", iteration+1)}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
		if _, lateErr := store.CompleteRunTerminal(ctx, CompleteRunCommand{Claim: grandchildClaim, ExpectedRunVersion: grandchildClaim.RunVersion, TargetState: statemachine.RunSucceeded, ResultHash: "late", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("late-attempt"), Child: &lateCompletion}); !errors.Is(lateErr, ErrExecutionRightConflict) {
			t.Fatalf("iteration %d late grandchild error=%v", iteration, lateErr)
		}
		lateCommitsRejected++

		var terminalRuns, cancellations, childEvents, rootEvents, resumeCommands, concurrent, allocated int
		err = admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id IN ($2,$3,$4) AND status='cancelled'),
			(SELECT count(*) FROM agent.run_cancellations WHERE tenant_id=$1 AND run_id IN ($2,$3,$4) AND status='settled'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id IN ($3,$4) AND event_type='ChildRunCompleted'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunCancelled'),
			(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id IN ($2,$3) AND command_type='ResumeParentRun'),
			(SELECT concurrent_children FROM agent.orchestration_quotas WHERE tenant_id=$1 AND root_run_id=$2),
			(SELECT allocated_budget_microunits FROM agent.orchestration_quotas WHERE tenant_id=$1 AND root_run_id=$2)`, tenantID, rootRunID, childRunID, grandchildRunID).Scan(&terminalRuns, &cancellations, &childEvents, &rootEvents, &resumeCommands, &concurrent, &allocated)
		if err != nil {
			t.Fatalf("iteration %d inspect: %v", iteration, err)
		}
		if terminalRuns != 3 || cancellations != 3 || childEvents != 2 || rootEvents != 1 || resumeCommands != 0 || concurrent != 0 || allocated != 0 {
			t.Fatalf("iteration %d terminal=%d cancellations=%d events=%d/%d resume=%d quota=%d/%d", iteration, terminalRuns, cancellations, childEvents, rootEvents, resumeCommands, concurrent, allocated)
		}
	}
	t.Logf("fault_injection={\"scenario\":\"recursive_run_cancellation\",\"repetitions\":%d,\"trees_cancelled\":%d,\"barriers_settled\":%d,\"propagated_barriers\":%d,\"propagation_replays\":%d,\"late_commits_rejected\":%d,\"duplicate_terminal_events\":0,\"unexpected_resumes\":0,\"quota_leaks\":0,\"lost_event_facts\":0}", repetitions, repetitions, repetitions*3, repetitions*2, propagationReplays, lateCommitsRejected)
}

func recursiveFaultUUID(value int) string {
	return fmt.Sprintf("fb100000-0000-4000-8000-%012d", value)
}
