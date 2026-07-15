//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestSpawnChildRunsIsAtomicQuotaBoundAndSingleWinner(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()

	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	const tenantID = "43100000-0000-4000-8000-000000000001"
	const userID = "43100000-0000-4000-8000-000000000002"
	const parentID = "43100000-0000-4000-8000-000000000003"
	const conversationID = "43100000-0000-4000-8000-000000000004"
	const correlationID = "43100000-0000-4000-8000-000000000005"
	const storeEpoch = "43100000-0000-4000-8000-000000000006"
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "child-spawn@example.invalid", now)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x43}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "child-spawn-test", Pepper: bytes.Repeat([]byte{0x44}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	pointer := func(name string) PayloadPointer {
		return PayloadPointer{Ref: "encrypted://child-spawn/" + name, Hash: "child-spawn-" + name}
	}
	parentStart := pointer("parent-start")
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: parentID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(2 * time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":64,"max_cost_microunits":10000}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: pointer("parent-accepted"), QueuedEvent: pointer("parent-queued"), StartCommand: parentStart, QueueClass: "interactive", ResourceClass: "llm", Priority: 80, CostUnits: 2, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: parentID, PayloadRef: parentStart.Ref, PayloadHash: parentStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: "child-spawn-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("parent-started"), AttemptStartedEvent: pointer("parent-attempt-started"), AttemptExpiredEvent: pointer("parent-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	child := func(id, suffix string, required bool, budget int64) ChildRunRequest {
		return ChildRunRequest{RunID: id, RequestHash: "request-" + suffix, DescriptorSnapshotID: "spawn_agent_run@sha256:contract", NormalizedInputRef: "encrypted://child-spawn/input-" + suffix, BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16,"max_cost_microunits":3000}`), BudgetMicrounits: budget, DueAt: now.Add(time.Hour), Required: required, QueueClass: "interactive", ResourceClass: "llm", Priority: 60, CostUnits: 1, MaxAttempts: 5, ToolSucceededEvent: pointer("tool-" + suffix), AcceptedEvent: pointer("accepted-" + suffix), QueuedEvent: pointer("queued-" + suffix), StartCommand: pointer("start-" + suffix)}
	}
	command := SpawnChildRunsCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, StepID: "parallel-delegation", JoinPolicy: "all", QuorumCount: 1, Children: []ChildRunRequest{child("43100000-0000-4000-8000-000000000011", "a", true, 2000), child("43100000-0000-4000-8000-000000000012", "b", false, 1000)}, PlanResultHash: "spawn-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: pointer("attempt-completed"), ParentWaitingEvent: pointer("parent-waiting"), AssistantMessage: &RunMessageInput{MessageID: "43100000-0000-4000-8000-000000000007", Message: PayloadPointer{Ref: "encrypted://child-spawn/assistant", Hash: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}, ContentHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", FinalizedEvent: pointer("message-finalized")}}

	const contenders = 12
	results := make(chan ChildRunsSpawned, contenders)
	errorsFound := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, spawnErr := store.SpawnChildRuns(ctx, command)
			if spawnErr != nil {
				errorsFound <- spawnErr
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	winners := 0
	var result ChildRunsSpawned
	for candidate := range results {
		winners++
		result = candidate
	}
	for spawnErr := range errorsFound {
		if !errors.Is(spawnErr, ErrExecutionRightConflict) {
			t.Fatalf("unexpected contender error: %v", spawnErr)
		}
	}
	if winners != 1 || len(result.Children) != 2 || result.ParentRunVersion != claim.RunVersion+1 || result.RootRunID != parentID || result.Depth != 1 {
		t.Fatalf("winners=%d result=%#v", winners, result)
	}

	var parentStatus, attemptStatus, inboxStatus, jobStatus string
	var parentVersion, children, tools, members, total, concurrent, allocated, parentEvents, startCommands, messages, messageEvents int
	err = admin.QueryRow(ctx, `SELECT r.status,r.run_version,a.status,i.status,j.status,
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2 AND root_run_id=$2 AND depth=1),
		(SELECT count(*) FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2 AND tool_name='spawn_agent_run' AND status='succeeded' AND execution_mode='inline_platform'),
		(SELECT count(*) FROM agent.child_group_members WHERE tenant_id=$1 AND group_id=$4),
		q.total_descendants,q.concurrent_children,q.allocated_budget_microunits,
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND aggregate_version=$5 AND event_type='ChildRunSpawned'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='StartAgentRun' AND aggregate_id IN ('43100000-0000-4000-8000-000000000011','43100000-0000-4000-8000-000000000012')),
		(SELECT count(*) FROM agent.run_messages WHERE tenant_id=$1 AND run_id=$2 AND id=$6 AND role='assistant' AND message_index=0),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run_message' AND aggregate_id=$6 AND event_type='RunMessageFinalized')
		FROM agent.runs r JOIN agent.job_attempts a ON a.tenant_id=r.tenant_id AND a.id=$3
		JOIN agent.inbox i ON i.tenant_id=a.tenant_id AND i.owner_attempt_id=a.id
		JOIN agent.jobs j ON j.tenant_id=a.tenant_id AND j.id=a.job_id
		JOIN agent.orchestration_quotas q ON q.tenant_id=r.tenant_id AND q.root_run_id=r.id
		WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, parentID, claim.AttemptID, result.GroupID, result.ParentRunVersion, command.AssistantMessage.MessageID).Scan(&parentStatus, &parentVersion, &attemptStatus, &inboxStatus, &jobStatus, &children, &tools, &members, &total, &concurrent, &allocated, &parentEvents, &startCommands, &messages, &messageEvents)
	if err != nil {
		t.Fatal(err)
	}
	if parentStatus != "waiting_child" || parentVersion != int(result.ParentRunVersion) || attemptStatus != "succeeded" || inboxStatus != "completed" || jobStatus != "succeeded" || children != 2 || tools != 2 || members != 2 || total != 2 || concurrent != 2 || allocated != 3000 || parentEvents != 1 || startCommands != 2 || messages != 1 || messageEvents != 1 {
		t.Fatalf("parent=%s/v%d attempt=%s inbox=%s job=%s children=%d tools=%d members=%d quota=%d/%d/%d event=%d starts=%d messages=%d message_events=%d", parentStatus, parentVersion, attemptStatus, inboxStatus, jobStatus, children, tools, members, total, concurrent, allocated, parentEvents, startCommands, messages, messageEvents)
	}

	const rejectedParent = "43100000-0000-4000-8000-000000000021"
	rejectedStart := pointer("rejected-parent-start")
	rejectedAccepted, err := store.Accept(ctx, AcceptRunCommand{RunID: rejectedParent, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(2 * time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_cost_microunits":1000}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: pointer("rejected-parent-accepted"), QueuedEvent: pointer("rejected-parent-queued"), StartCommand: rejectedStart, QueueClass: "interactive", ResourceClass: "llm", Priority: 80, CostUnits: 2, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	rejectedClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: rejectedAccepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: rejectedParent, PayloadRef: rejectedStart.Ref, PayloadHash: rejectedStart.Hash}, ConsumerName: "agent-run-worker", WorkerID: "rejected-child-spawn-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("rejected-parent-started"), AttemptStartedEvent: pointer("rejected-parent-attempt-started"), AttemptExpiredEvent: pointer("rejected-parent-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	rejectedCommand := command
	rejectedCommand.Claim, rejectedCommand.ExpectedRunVersion = rejectedClaim, rejectedClaim.RunVersion
	rejectedCommand.StepID = "over-budget"
	rejectedCommand.Children = []ChildRunRequest{child("43100000-0000-4000-8000-000000000022", "over-budget", true, 1001)}
	if _, err = store.SpawnChildRuns(ctx, rejectedCommand); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("over-budget spawn error=%v", err)
	}
	var rejectedStatus string
	var residue int
	if err = admin.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2)+(SELECT count(*) FROM agent.child_groups WHERE tenant_id=$1 AND parent_run_id=$2)+(SELECT count(*) FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2 AND tool_name='spawn_agent_run')+(SELECT count(*) FROM agent.orchestration_quotas WHERE tenant_id=$1 AND root_run_id=$2) FROM agent.runs WHERE tenant_id=$1 AND id=$2`, tenantID, rejectedParent).Scan(&rejectedStatus, &residue); err != nil || rejectedStatus != "executing" || residue != 0 {
		t.Fatalf("rejected parent=%s residue=%d error=%v", rejectedStatus, residue, err)
	}
}
