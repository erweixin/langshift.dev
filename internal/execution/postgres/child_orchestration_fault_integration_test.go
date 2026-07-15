//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestChildOrchestrationFaultInjection100(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "fa000000-0000-4000-8000-000000000001"
	const userID = "fa000000-0000-4000-8000-000000000002"
	const storeEpoch = "fa000000-0000-4000-8000-000000000003"
	start := time.Date(2026, time.July, 15, 20, 0, 0, 0, time.UTC)
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "child-orchestration-fault@example.invalid", start)
	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloadsStore := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "child-orchestration-fault-v1", Material: bytes.Repeat([]byte{0xfa}, 32)}}, Blobs: blobs}

	const repetitions = 100
	var allRuns, anyRuns, quorumRuns, cleanupCommands, lateCommitsRejected int
	for iteration := 0; iteration < repetitions; iteration++ {
		clock := start.Add(time.Duration(iteration) * time.Minute)
		policy := []string{"all", "any", "quorum"}[iteration%3]
		quorum := 3
		initial := []int{0, 1, 2}
		switch policy {
		case "all":
			allRuns++
		case "any":
			anyRuns++
			quorum, initial = 1, []int{0}
		case "quorum":
			quorumRuns++
			quorum, initial = 2, []int{0, 1}
		}
		base := 1000 + iteration*100
		rootRunID := orchestrationFaultUUID(base + 1)
		conversationID := orchestrationFaultUUID(base + 2)
		correlationID := orchestrationFaultUUID(base + 3)
		children := []string{orchestrationFaultUUID(base + 10), orchestrationFaultUUID(base + 11), orchestrationFaultUUID(base + 12)}
		store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }}, IDKey: bytes.Repeat([]byte{0xf8}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return clock }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "child-orchestration-fault", Pepper: bytes.Repeat([]byte{0xf9}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
		pointer := func(name string) PayloadPointer {
			return PayloadPointer{Ref: "encrypted://child-fault/" + rootRunID + "/" + name, Hash: "child-fault-" + name}
		}

		rootStart := pointer("root-start")
		accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: rootRunID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: clock.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":10000}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: pointer("root-accepted"), QueuedEvent: pointer("root-queued"), StartCommand: rootStart, QueueClass: "interactive", ResourceClass: "llm", Priority: 80, CostUnits: 1, MaxAttempts: 5})
		if err != nil {
			t.Fatalf("iteration %d accept: %v", iteration, err)
		}
		parentClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: rootRunID, PayloadRef: rootStart.Ref, PayloadHash: rootStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: fmt.Sprintf("parent-%03d", iteration), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("root-started"), AttemptStartedEvent: pointer("root-attempt-started"), AttemptExpiredEvent: pointer("root-attempt-expired")})
		if err != nil {
			t.Fatalf("iteration %d claim parent: %v", iteration, err)
		}
		requests := make([]ChildRunRequest, len(children))
		starts := make([]PayloadPointer, len(children))
		for index, childID := range children {
			suffix := fmtIndex(index)
			starts[index] = pointer("child-start-" + suffix)
			requests[index] = ChildRunRequest{RunID: childID, RequestHash: fmt.Sprintf("request-%03d-%d", iteration, index), DescriptorSnapshotID: "spawn_agent_run@sha256:contract", NormalizedInputRef: pointer("input-" + suffix).Ref, BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":1000}`), BudgetMicrounits: 1000, DueAt: clock.Add(30 * time.Minute), Required: true, QueueClass: "interactive", ResourceClass: "llm", Priority: 60, CostUnits: 1, MaxAttempts: 5, ToolSucceededEvent: pointer("tool-" + suffix), AcceptedEvent: pointer("accepted-" + suffix), QueuedEvent: pointer("queued-" + suffix), StartCommand: starts[index]}
		}
		spawned, err := store.SpawnChildRuns(ctx, SpawnChildRunsCommand{Claim: parentClaim, ExpectedRunVersion: parentClaim.RunVersion, StepID: fmt.Sprintf("fault-%03d", iteration), JoinPolicy: policy, QuorumCount: quorum, Children: requests, PlanResultHash: fmt.Sprintf("plan-%03d", iteration), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("parent-attempt-completed"), ParentWaitingEvent: pointer("parent-waiting")})
		if err != nil || len(spawned.Children) != 3 {
			t.Fatalf("iteration %d spawn=%#v error=%v", iteration, spawned, err)
		}
		claims := make([]RunClaim, 3)
		for index, childID := range children {
			claims[index], err = store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: spawned.Children[index].StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: childID, PayloadRef: starts[index].Ref, PayloadHash: starts[index].Hash}, ConsumerName: "agent-run-worker", WorkerID: fmt.Sprintf("child-%03d-%d", iteration, index), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("child-started-" + fmtIndex(index)), AttemptStartedEvent: pointer("child-attempt-started-" + fmtIndex(index)), AttemptExpiredEvent: pointer("child-attempt-expired-" + fmtIndex(index))})
			if err != nil {
				t.Fatalf("iteration %d claim child %d: %v", iteration, index, err)
			}
		}
		complete := func(index int) (CompletedRun, error) {
			suffix := fmtIndex(index)
			completion := ChildRunCompletion{ResultSummary: PayloadPointer{Ref: pointer("summary-" + suffix).Ref, Hash: strings.Repeat(fmtIndex(index+1), 64)}, CompletedEvent: pointer("child-completed-" + suffix), GroupJoinedEvent: pointer("group-joined"), RunResumeQueuedEvent: pointer("parent-resume"), ResumeCommand: pointer("resume-command"), CancelRemainingCommand: PayloadPointer{Ref: pointer("cancel-remaining").Ref, Hash: strings.Repeat("c", 64)}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 70, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
			return store.CompleteRunTerminal(ctx, CompleteRunCommand{Claim: claims[index], ExpectedRunVersion: claims[index].RunVersion, TargetState: statemachine.RunSucceeded, ResultHash: "success", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("child-attempt-completed-" + suffix), Child: &completion})
		}
		var wait sync.WaitGroup
		resultChannel := make(chan CompletedRun, len(initial))
		errorChannel := make(chan error, len(initial))
		for _, index := range initial {
			index := index
			wait.Add(1)
			go func() {
				defer wait.Done()
				completed, completeErr := complete(index)
				if completeErr != nil {
					errorChannel <- completeErr
					return
				}
				resultChannel <- completed
			}()
		}
		wait.Wait()
		close(resultChannel)
		close(errorChannel)
		for completeErr := range errorChannel {
			t.Fatalf("iteration %d concurrent completion: %v", iteration, completeErr)
		}
		resumed := 0
		for completed := range resultChannel {
			if completed.ParentResumed {
				resumed++
			}
		}
		if resumed != 1 {
			t.Fatalf("iteration %d policy=%s resumed=%d", iteration, policy, resumed)
		}

		if policy == "all" {
			if _, lateErr := complete(0); !errors.Is(lateErr, ErrExecutionRightConflict) {
				t.Fatalf("iteration %d duplicate completion error=%v", iteration, lateErr)
			}
			lateCommitsRejected++
		} else {
			cleanupCommands++
			reconciler := RunCancellationReconcilerService{Store: store, Payloads: payloadsStore, IDKey: store.IDKey}
			workIDs, listErr := reconciler.ListDueChildGroupCancellationIDs(ctx, tenantID, storeEpoch, "", 10)
			if listErr != nil || len(workIDs) != 1 {
				t.Fatalf("iteration %d cleanup ids=%v error=%v", iteration, workIDs, listErr)
			}
			cleanup, cleanupErr := reconciler.ReconcileDueChildGroupCancellation(ctx, tenantID, workIDs[0], storeEpoch)
			if cleanupErr != nil || !cleanup.Complete || cleanup.RequestedChildren != 3-len(initial) {
				t.Fatalf("iteration %d cleanup=%#v error=%v", iteration, cleanup, cleanupErr)
			}
			if replay, replayErr := reconciler.ReconcileDueChildGroupCancellation(ctx, tenantID, workIDs[0], storeEpoch); replayErr != nil || !replay.Replayed || !replay.Complete {
				t.Fatalf("iteration %d cleanup replay=%#v error=%v", iteration, replay, replayErr)
			}
			initialSet := map[int]bool{}
			for _, index := range initial {
				initialSet[index] = true
			}
			for index, childID := range children {
				if initialSet[index] {
					continue
				}
				if _, lateErr := complete(index); !errors.Is(lateErr, ErrExecutionRightConflict) {
					t.Fatalf("iteration %d late child %d error=%v", iteration, index, lateErr)
				}
				lateCommitsRejected++
				clock = clock.Add(cancellationReconciliationDelay)
				cancellationID, identifierErr := store.childGroupRemainderRunCancellationID(spawned.GroupID, childID)
				if identifierErr != nil {
					t.Fatal(identifierErr)
				}
				settled, settleErr := reconciler.ReconcileDueCancellation(ctx, tenantID, cancellationID, storeEpoch)
				if settleErr != nil || !settled.Settled {
					t.Fatalf("iteration %d settle child %d=%#v error=%v", iteration, index, settled, settleErr)
				}
			}
		}

		var continuations, childEvents, groupEvents, parentEvents, quotaConcurrent, quotaBudget, cleanupRows, terminalChildren int
		err = admin.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM agent.continuations WHERE tenant_id=$1 AND group_kind='child' AND group_id=$3 AND status='committed'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=ANY($4::uuid[]) AND event_type='ChildRunCompleted'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='child_group' AND aggregate_id=$3 AND event_type='ChildGroupJoined'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunResumeQueued'),
			(SELECT concurrent_children FROM agent.orchestration_quotas WHERE tenant_id=$1 AND root_run_id=$2),
			(SELECT allocated_budget_microunits FROM agent.orchestration_quotas WHERE tenant_id=$1 AND root_run_id=$2),
			(SELECT count(*) FROM agent.child_group_cancellations WHERE tenant_id=$1 AND group_id=$3),
			(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=ANY($4::uuid[]) AND status IN ('succeeded','cancelled'))`, tenantID, rootRunID, spawned.GroupID, children).Scan(&continuations, &childEvents, &groupEvents, &parentEvents, &quotaConcurrent, &quotaBudget, &cleanupRows, &terminalChildren)
		if err != nil {
			t.Fatalf("iteration %d inspect: %v", iteration, err)
		}
		expectedCleanup := 0
		if policy != "all" {
			expectedCleanup = 1
		}
		if continuations != 1 || childEvents != 3 || groupEvents != 1 || parentEvents != 1 || quotaConcurrent != 0 || quotaBudget != 0 || cleanupRows != expectedCleanup || terminalChildren != 3 {
			t.Fatalf("iteration %d policy=%s continuation=%d events=%d/%d/%d quota=%d/%d cleanup=%d terminal=%d", iteration, policy, continuations, childEvents, groupEvents, parentEvents, quotaConcurrent, quotaBudget, cleanupRows, terminalChildren)
		}
	}
	t.Logf("fault_injection={\"scenario\":\"child_orchestration_race\",\"repetitions\":%d,\"all\":%d,\"any\":%d,\"quorum\":%d,\"atomic_spawns\":%d,\"unique_continuations\":%d,\"cleanup_commands\":%d,\"late_commits_rejected\":%d,\"duplicate_continuations\":0,\"quota_leaks\":0,\"lost_event_facts\":0}", repetitions, allRuns, anyRuns, quorumRuns, repetitions, repetitions, cleanupCommands, lateCommitsRejected)
}

func orchestrationFaultUUID(value int) string {
	return fmt.Sprintf("fa100000-0000-4000-8000-%012d", value)
}
