//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestPropagateRunCancellationBuildsBoundedRecursiveLineage(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()

	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	const tenantID = "43300000-0000-4000-8000-000000000001"
	const userID = "43300000-0000-4000-8000-000000000002"
	const conversationID = "43300000-0000-4000-8000-000000000003"
	const correlationID = "43300000-0000-4000-8000-000000000004"
	const storeEpoch = "43300000-0000-4000-8000-000000000005"
	const rootRunID = "43300000-0000-4000-8000-000000000010"
	const childRunID = "43300000-0000-4000-8000-000000000011"
	const grandchildRunID = "43300000-0000-4000-8000-000000000012"
	const rootCancellationID = "43300000-0000-4000-8000-000000000020"
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "propagation@example.invalid", now)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x58}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "cancellation-propagation-test", Pepper: bytes.Repeat([]byte{0x59}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	pointer := func(name string) PayloadPointer {
		return PayloadPointer{Ref: "encrypted://propagation/" + name, Hash: "propagation-" + name}
	}

	rootStart := pointer("root-start")
	rootAccepted, err := store.Accept(ctx, AcceptRunCommand{RunID: rootRunID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(4 * time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":10000}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: pointer("root-accepted"), QueuedEvent: pointer("root-queued"), StartCommand: rootStart, QueueClass: "interactive", ResourceClass: "llm", Priority: 80, CostUnits: 1, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	rootClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: rootAccepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: rootRunID, PayloadRef: rootStart.Ref, PayloadHash: rootStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: "root-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("root-started"), AttemptStartedEvent: pointer("root-attempt-started"), AttemptExpiredEvent: pointer("root-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	childStart := pointer("child-start")
	rootSpawn, err := store.SpawnChildRuns(ctx, SpawnChildRunsCommand{Claim: rootClaim, ExpectedRunVersion: rootClaim.RunVersion, StepID: "root-child", JoinPolicy: "all", QuorumCount: 1, Children: []ChildRunRequest{{RunID: childRunID, RequestHash: "root-child-request", DescriptorSnapshotID: "spawn_agent_run@sha256:contract", NormalizedInputRef: "encrypted://propagation/root-child", BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":4000}`), BudgetMicrounits: 4000, DueAt: now.Add(3 * time.Hour), Required: true, QueueClass: "interactive", ResourceClass: "llm", Priority: 70, CostUnits: 1, MaxAttempts: 5, ToolSucceededEvent: pointer("root-child-tool"), AcceptedEvent: pointer("child-accepted"), QueuedEvent: pointer("child-queued"), StartCommand: childStart}}, PlanResultHash: "root-child-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("root-attempt-completed"), ParentWaitingEvent: pointer("root-waiting")})
	if err != nil {
		t.Fatal(err)
	}
	childClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: rootSpawn.Children[0].StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: childRunID, PayloadRef: childStart.Ref, PayloadHash: childStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: "child-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("child-started"), AttemptStartedEvent: pointer("child-attempt-started"), AttemptExpiredEvent: pointer("child-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	grandchildStart := pointer("grandchild-start")
	childSpawn, err := store.SpawnChildRuns(ctx, SpawnChildRunsCommand{Claim: childClaim, ExpectedRunVersion: childClaim.RunVersion, StepID: "child-grandchild", JoinPolicy: "all", QuorumCount: 1, Children: []ChildRunRequest{{RunID: grandchildRunID, RequestHash: "grandchild-request", DescriptorSnapshotID: "spawn_agent_run@sha256:contract", NormalizedInputRef: "encrypted://propagation/grandchild", BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":1000}`), BudgetMicrounits: 1000, DueAt: now.Add(2 * time.Hour), Required: true, QueueClass: "interactive", ResourceClass: "llm", Priority: 60, CostUnits: 1, MaxAttempts: 5, ToolSucceededEvent: pointer("grandchild-tool"), AcceptedEvent: pointer("grandchild-accepted"), QueuedEvent: pointer("grandchild-queued"), StartCommand: grandchildStart}}, PlanResultHash: "grandchild-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("child-attempt-completed"), ParentWaitingEvent: pointer("child-waiting")})
	if err != nil {
		t.Fatal(err)
	}

	cancel := cancellationTestCommand(rootCancellationID, tenantID, userID, rootRunID, correlationID, rootSpawn.ParentRunVersion, "a")
	requested, err := store.RequestCancellation(ctx, cancel)
	if err != nil || requested.Settled || requested.ReconcileCommandID == "" {
		t.Fatalf("root cancellation=%#v error=%v", requested, err)
	}
	recoveryStore := store
	recoveryNow := now.Add(cancellationReconciliationDelay)
	recoveryStore.Now = func() time.Time { return recoveryNow }
	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloadsStore := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "recursive-cancellation-v1", Material: bytes.Repeat([]byte{0x5a}, 32)}}, Blobs: blobs}
	reconciler := RunCancellationReconcilerService{Store: recoveryStore, Payloads: payloadsStore, IDKey: store.IDKey}
	rootDeferred, err := reconciler.ReconcileDueCancellation(ctx, tenantID, rootCancellationID, storeEpoch)
	if err != nil || rootDeferred.Settled || rootDeferred.CancellationVersion != 3 {
		t.Fatalf("root service propagation=%#v error=%v", rootDeferred, err)
	}
	recoveryNow = recoveryNow.Add(cancellationReconciliationDelay)
	childCancellationID, err := store.propagatedCancellationID(rootCancellationID, childRunID)
	if err != nil {
		t.Fatal(err)
	}
	childDeferred, err := reconciler.ReconcileDueCancellation(ctx, tenantID, childCancellationID, storeEpoch)
	if err != nil || childDeferred.Settled || childDeferred.CancellationVersion != 3 {
		t.Fatalf("child service propagation=%#v error=%v", childDeferred, err)
	}
	recoveryNow = recoveryNow.Add(cancellationReconciliationDelay)
	grandchildCancellationID, err := store.propagatedCancellationID(rootCancellationID, grandchildRunID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.PropagateRunCancellation(ctx, PropagateRunCancellationCommand{CancellationID: rootCancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedCancellationVersion: 2, BatchSize: 10, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, Children: map[string]PropagatedRunCancellationPayloads{}})
	if err != nil || !replay.Replayed || !replay.Complete || replay.CancellationVersion != 3 || replay.NextCursor != childRunID {
		t.Fatalf("root propagation replay=%#v error=%v", replay, err)
	}

	var cancellations, propagateOutbox, reconcileOutbox, propagateJobs, reconcileJobs, cancelledPropagationJobs, cancelledRuns int
	var childParent, childRoot, grandchildParent, grandchildRoot string
	err = admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.run_cancellations WHERE tenant_id=$1),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='PropagateRunCancellation'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='ReconcileRunCancellation'),
		(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND resource_class='run-cancellation-propagation'),
		(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND resource_class='run-cancellation'),
		(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND resource_class='run-cancellation-propagation' AND status='cancelled'),
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND cancel_requested_at IS NOT NULL),
		(SELECT parent_cancellation_id::text FROM agent.run_cancellations WHERE tenant_id=$1 AND id=$2),
		(SELECT root_cancellation_id::text FROM agent.run_cancellations WHERE tenant_id=$1 AND id=$2),
		(SELECT parent_cancellation_id::text FROM agent.run_cancellations WHERE tenant_id=$1 AND id=$3),
		(SELECT root_cancellation_id::text FROM agent.run_cancellations WHERE tenant_id=$1 AND id=$3)`, tenantID, childCancellationID, grandchildCancellationID).Scan(&cancellations, &propagateOutbox, &reconcileOutbox, &propagateJobs, &reconcileJobs, &cancelledPropagationJobs, &cancelledRuns, &childParent, &childRoot, &grandchildParent, &grandchildRoot)
	if err != nil {
		t.Fatal(err)
	}
	if cancellations != 3 || propagateOutbox != 2 || reconcileOutbox != 3 || propagateJobs != 2 || reconcileJobs != 3 || cancelledPropagationJobs != 2 || cancelledRuns != 3 || childParent != rootCancellationID || childRoot != rootCancellationID || grandchildParent != childCancellationID || grandchildRoot != rootCancellationID {
		t.Fatalf("cancellations=%d outbox=%d/%d jobs=%d/%d cancelled_jobs=%d cancelled_runs=%d child=%s/%s grandchild=%s/%s", cancellations, propagateOutbox, reconcileOutbox, propagateJobs, reconcileJobs, cancelledPropagationJobs, cancelledRuns, childParent, childRoot, grandchildParent, grandchildRoot)
	}
	if childSpawn.ParentRunID != childRunID {
		t.Fatalf("unexpected child spawn result: %#v", childSpawn)
	}

	for _, current := range []string{grandchildCancellationID, childCancellationID, rootCancellationID} {
		reconciled, reconcileErr := reconciler.ReconcileDueCancellation(ctx, tenantID, current, storeEpoch)
		if reconcileErr != nil || !reconciled.Settled {
			t.Fatalf("reconcile %s=%#v error=%v", current, reconciled, reconcileErr)
		}
	}

	var terminalRuns, settledCancellations, childEvents, rootEvents, resumeCommands, liveQuota, allocatedBudget int
	if err = admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id IN ($2,$3,$4) AND status='cancelled'),
		(SELECT count(*) FROM agent.run_cancellations WHERE tenant_id=$1 AND status='settled'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND event_type='ChildRunCompleted' AND aggregate_id IN ($3,$4)),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND event_type='RunCancelled' AND aggregate_id=$2),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='ResumeParentRun'),
		(SELECT concurrent_children FROM agent.orchestration_quotas WHERE tenant_id=$1 AND root_run_id=$2),
		(SELECT allocated_budget_microunits FROM agent.orchestration_quotas WHERE tenant_id=$1 AND root_run_id=$2)`, tenantID, rootRunID, childRunID, grandchildRunID).Scan(&terminalRuns, &settledCancellations, &childEvents, &rootEvents, &resumeCommands, &liveQuota, &allocatedBudget); err != nil {
		t.Fatal(err)
	}
	if terminalRuns != 3 || settledCancellations != 3 || childEvents != 2 || rootEvents != 1 || resumeCommands != 0 || liveQuota != 0 || allocatedBudget != 0 {
		t.Fatalf("terminal_runs=%d cancellations=%d events=%d/%d resume=%d quota=%d/%d", terminalRuns, settledCancellations, childEvents, rootEvents, resumeCommands, liveQuota, allocatedBudget)
	}
}
