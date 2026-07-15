//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestChildGroupJoinPoliciesResumeParentExactlyOnce(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()

	now := time.Date(2026, time.July, 15, 14, 0, 0, 0, time.UTC)
	const tenantID = "43200000-0000-4000-8000-000000000001"
	const userID = "43200000-0000-4000-8000-000000000002"
	const conversationID = "43200000-0000-4000-8000-000000000003"
	const correlationID = "43200000-0000-4000-8000-000000000004"
	const storeEpoch = "43200000-0000-4000-8000-000000000005"
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "child-join@example.invalid", now)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x56}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "child-join-test", Pepper: bytes.Repeat([]byte{0x57}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}

	tests := []struct {
		name, parentID, policy string
		children               []string
		requiredCount, quorum  int
		initial                []int
		states                 []statemachine.RunState
	}{
		{name: "all", parentID: "43200000-0000-4000-8000-000000000100", policy: "all", children: []string{"43200000-0000-4000-8000-000000000101", "43200000-0000-4000-8000-000000000102", "43200000-0000-4000-8000-000000000103"}, requiredCount: 2, quorum: 2, initial: []int{0, 1}, states: []statemachine.RunState{statemachine.RunFailed, statemachine.RunSucceeded, statemachine.RunSucceeded}},
		{name: "any", parentID: "43200000-0000-4000-8000-000000000200", policy: "any", children: []string{"43200000-0000-4000-8000-000000000201", "43200000-0000-4000-8000-000000000202", "43200000-0000-4000-8000-000000000203"}, requiredCount: 3, quorum: 1, initial: []int{0}, states: []statemachine.RunState{statemachine.RunSucceeded, statemachine.RunFailed, statemachine.RunSucceeded}},
		{name: "quorum", parentID: "43200000-0000-4000-8000-000000000300", policy: "quorum", children: []string{"43200000-0000-4000-8000-000000000301", "43200000-0000-4000-8000-000000000302", "43200000-0000-4000-8000-000000000303"}, requiredCount: 3, quorum: 2, initial: []int{0, 1}, states: []statemachine.RunState{statemachine.RunSucceeded, statemachine.RunSucceeded, statemachine.RunFailed}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pointer := func(name string) PayloadPointer {
				return PayloadPointer{Ref: "encrypted://child-join/" + test.name + "/" + name, Hash: test.name + "-" + name}
			}
			parentStart := pointer("parent-start")
			accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: test.parentID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(2 * time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":64,"max_cost_microunits":10000}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: pointer("parent-accepted"), QueuedEvent: pointer("parent-queued"), StartCommand: parentStart, QueueClass: "interactive", ResourceClass: "llm", Priority: 80, CostUnits: 2, MaxAttempts: 5})
			if err != nil {
				t.Fatal(err)
			}
			parentClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: test.parentID, PayloadRef: parentStart.Ref, PayloadHash: parentStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: "parent-" + test.name, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("parent-started"), AttemptStartedEvent: pointer("parent-attempt-started"), AttemptExpiredEvent: pointer("parent-attempt-expired")})
			if err != nil {
				t.Fatal(err)
			}
			requests := make([]ChildRunRequest, len(test.children))
			starts := make([]PayloadPointer, len(test.children))
			for index, childID := range test.children {
				suffix := fmtIndex(index)
				starts[index] = pointer("child-start-" + suffix)
				requests[index] = ChildRunRequest{RunID: childID, RequestHash: "request-" + test.name + "-" + suffix, DescriptorSnapshotID: "spawn_agent_run@sha256:contract", NormalizedInputRef: "encrypted://child-join/input-" + test.name + "-" + suffix, BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16,"max_cost_microunits":1000}`), BudgetMicrounits: 1000, DueAt: now.Add(time.Hour), Required: index < test.requiredCount, QueueClass: "interactive", ResourceClass: "llm", Priority: 60, CostUnits: 1, MaxAttempts: 5, ToolSucceededEvent: pointer("tool-" + suffix), AcceptedEvent: pointer("child-accepted-" + suffix), QueuedEvent: pointer("child-queued-" + suffix), StartCommand: starts[index]}
			}
			spawned, err := store.SpawnChildRuns(ctx, SpawnChildRunsCommand{Claim: parentClaim, ExpectedRunVersion: parentClaim.RunVersion, StepID: "join-" + test.name, JoinPolicy: test.policy, QuorumCount: test.quorum, Children: requests, PlanResultHash: "spawn-plan-" + test.name, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("parent-attempt-completed"), ParentWaitingEvent: pointer("parent-waiting")})
			if err != nil {
				t.Fatal(err)
			}
			claims := make([]RunClaim, len(test.children))
			for index, childID := range test.children {
				claims[index], err = store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: spawned.Children[index].StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: childID, PayloadRef: starts[index].Ref, PayloadHash: starts[index].Hash}, ConsumerName: "agent-run-worker", WorkerID: "child-" + test.name + "-" + fmtIndex(index), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("child-started-" + fmtIndex(index)), AttemptStartedEvent: pointer("child-attempt-started-" + fmtIndex(index)), AttemptExpiredEvent: pointer("child-attempt-expired-" + fmtIndex(index))})
				if err != nil {
					t.Fatal(err)
				}
			}

			complete := func(index int) (CompletedRun, error) {
				suffix := fmtIndex(index)
				completion := ChildRunCompletion{ResultSummary: PayloadPointer{Ref: "encrypted://child-join/" + test.name + "/summary-" + suffix, Hash: strings.Repeat(fmtIndex(index+1), 64)}, CompletedEvent: pointer("child-completed-" + suffix), GroupJoinedEvent: pointer("group-joined"), RunResumeQueuedEvent: pointer("parent-resume-queued"), ResumeCommand: pointer("resume-parent"), CancelRemainingCommand: PayloadPointer{Ref: "encrypted://child-join/" + test.name + "/cancel-remaining", Hash: strings.Repeat("c", 64)}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 70, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
				return store.CompleteRunTerminal(ctx, CompleteRunCommand{Claim: claims[index], ExpectedRunVersion: claims[index].RunVersion, TargetState: test.states[index], ResultHash: "result-" + test.name + "-" + suffix, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("child-attempt-completed-" + suffix), Child: &completion})
			}

			results := make(chan CompletedRun, len(test.initial))
			errorsFound := make(chan error, len(test.initial))
			var wait sync.WaitGroup
			for _, index := range test.initial {
				index := index
				wait.Add(1)
				go func() {
					defer wait.Done()
					result, completeErr := complete(index)
					if completeErr != nil {
						errorsFound <- completeErr
						return
					}
					results <- result
				}()
			}
			wait.Wait()
			close(results)
			close(errorsFound)
			for completeErr := range errorsFound {
				t.Fatalf("initial child completion: %v", completeErr)
			}
			resumed, parentVersion := 0, uint64(0)
			for result := range results {
				if result.ParentResumed {
					resumed++
					parentVersion = result.ParentRunVersion
				}
			}
			if resumed != 1 || parentVersion != spawned.ParentRunVersion+1 {
				t.Fatalf("initial join resumed=%d parent_version=%d spawned=%d", resumed, parentVersion, spawned.ParentRunVersion)
			}

			initialSet := map[int]bool{}
			for _, index := range test.initial {
				initialSet[index] = true
			}
			expectedCleanup := 0
			if test.policy == "all" {
				for index := range test.children {
					if initialSet[index] {
						continue
					}
					late, lateErr := complete(index)
					if lateErr != nil {
						t.Fatal(lateErr)
					}
					if late.ParentResumed || late.ParentRunVersion != 0 || late.ResumeCommandID != "" {
						t.Fatalf("late child resumed parent: %#v", late)
					}
				}
			} else {
				expectedCleanup = 1
				recoveryNow := now
				recoveryStore := store
				recoveryStore.Now = func() time.Time { return recoveryNow }
				blobs := &repairServiceBlobs{values: map[string][]byte{}}
				payloadsStore := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "child-group-cancellation-v1", Material: bytes.Repeat([]byte{0x61}, 32)}}, Blobs: blobs}
				reconciler := RunCancellationReconcilerService{Store: recoveryStore, Payloads: payloadsStore, IDKey: store.IDKey}
				dueTenants, tenantErr := reconciler.ListDueTenantIDs(ctx, storeEpoch, "", 10, 0, 1)
				if tenantErr != nil || len(dueTenants) != 1 || dueTenants[0] != tenantID {
					t.Fatalf("remainder cancellation tenants=%v error=%v", dueTenants, tenantErr)
				}
				workIDs, listErr := reconciler.ListDueChildGroupCancellationIDs(ctx, tenantID, storeEpoch, "", 10)
				if listErr != nil || len(workIDs) != 1 {
					t.Fatalf("remainder work ids=%v error=%v", workIDs, listErr)
				}
				cancelled, cancelErr := reconciler.ReconcileDueChildGroupCancellation(ctx, tenantID, workIDs[0], storeEpoch)
				if cancelErr != nil || !cancelled.Complete || cancelled.RequestedChildren != len(test.children)-len(test.initial) {
					t.Fatalf("remainder cancellation=%#v error=%v", cancelled, cancelErr)
				}
				replayed, replayErr := reconciler.ReconcileDueChildGroupCancellation(ctx, tenantID, workIDs[0], storeEpoch)
				if replayErr != nil || !replayed.Complete || !replayed.Replayed || replayed.Version != cancelled.Version {
					t.Fatalf("remainder cancellation replay=%#v error=%v", replayed, replayErr)
				}
				for index := range test.children {
					if initialSet[index] {
						continue
					}
					if late, lateErr := complete(index); !errors.Is(lateErr, ErrExecutionRightConflict) || late.RunID != "" {
						t.Fatalf("unneeded child committed late result: result=%#v error=%v", late, lateErr)
					}
				}
				recoveryNow = recoveryNow.Add(cancellationReconciliationDelay)
				for index, childID := range test.children {
					if initialSet[index] {
						continue
					}
					cancellationID, identifierErr := store.childGroupRemainderRunCancellationID(spawned.GroupID, childID)
					if identifierErr != nil {
						t.Fatal(identifierErr)
					}
					reconciled, reconcileErr := reconciler.ReconcileDueCancellation(ctx, tenantID, cancellationID, storeEpoch)
					if reconcileErr != nil || !reconciled.Settled {
						t.Fatalf("reconcile unneeded child %s=%#v error=%v", childID, reconciled, reconcileErr)
					}
				}
			}

			var parentStatus, pendingCommand, continuationID, joinedEventID string
			var durableParentVersion, groupVersion, continuations, resumeOutbox, resumeJobs, childEvents, legacyChildEvents, groupEvents, parentResumeEvents, concurrent, allocated, total, summaries, cleanupRows, cleanupOutbox int
			err = admin.QueryRow(ctx, `SELECT p.status,p.run_version,p.pending_command_id::text,g.version,g.continuation_id::text,g.joined_event_id::text,
				(SELECT count(*) FROM agent.continuations WHERE tenant_id=$1 AND group_kind='child' AND group_id=$3 AND continuation_kind='resume_parent' AND status='committed'),
				(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='ResumeParentRun' AND command_id=p.pending_command_id),
				(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND command_id=p.pending_command_id AND status='pending'),
				(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=ANY($4::uuid[]) AND event_type='ChildRunCompleted'),
				(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=ANY($4::uuid[]) AND event_type IN ('RunSucceeded','RunFailed','RunCancelled','RunExpired')),
				(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='child_group' AND aggregate_id=$3 AND event_type='ChildGroupJoined'),
				(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunResumeQueued'),
				q.concurrent_children,q.allocated_budget_microunits,q.total_descendants,
				(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=ANY($4::uuid[]) AND result_summary_ref IS NOT NULL AND result_summary_hash IS NOT NULL)
				,(SELECT count(*) FROM agent.child_group_cancellations WHERE tenant_id=$1 AND group_id=$3)
				,(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='CancelRemainingChildRuns' AND aggregate_id=$2)
				FROM agent.runs p JOIN agent.child_groups g ON g.tenant_id=p.tenant_id AND g.id=$3 JOIN agent.orchestration_quotas q ON q.tenant_id=p.tenant_id AND q.root_run_id=p.id WHERE p.tenant_id=$1 AND p.id=$2 AND g.joined=true`, tenantID, test.parentID, spawned.GroupID, test.children).Scan(&parentStatus, &durableParentVersion, &pendingCommand, &groupVersion, &continuationID, &joinedEventID, &continuations, &resumeOutbox, &resumeJobs, &childEvents, &legacyChildEvents, &groupEvents, &parentResumeEvents, &concurrent, &allocated, &total, &summaries, &cleanupRows, &cleanupOutbox)
			if err != nil {
				t.Fatal(err)
			}
			if parentStatus != "queued" || durableParentVersion != int(parentVersion) || pendingCommand == "" || groupVersion != 2 || continuationID == "" || joinedEventID == "" || continuations != 1 || resumeOutbox != 1 || resumeJobs != 1 || childEvents != len(test.children) || legacyChildEvents != 0 || groupEvents != 1 || parentResumeEvents != 1 || concurrent != 0 || allocated != 0 || total != len(test.children) || summaries != len(test.children) || cleanupRows != expectedCleanup || cleanupOutbox != expectedCleanup {
				t.Fatalf("parent=%s/v%d pending=%s group=v%d/%s/%s continuation=%d resume=%d/%d child_events=%d legacy=%d group_events=%d parent_events=%d quota=%d/%d/%d summaries=%d cleanup=%d/%d", parentStatus, durableParentVersion, pendingCommand, groupVersion, continuationID, joinedEventID, continuations, resumeOutbox, resumeJobs, childEvents, legacyChildEvents, groupEvents, parentResumeEvents, concurrent, allocated, total, summaries, cleanupRows, cleanupOutbox)
			}
		})
	}
}

func fmtIndex(index int) string {
	return string(rune('1' + index))
}
