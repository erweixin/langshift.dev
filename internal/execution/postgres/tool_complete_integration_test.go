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
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestConcurrentToolCompletionCreatesOneCommittedContinuation(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "5a100000-0000-4000-8000-000000000001"
	const userID = "5a100000-0000-4000-8000-000000000002"
	const runID = "5a100000-0000-4000-8000-000000000003"
	const conversationID = "5a100000-0000-4000-8000-000000000004"
	const correlationID = "5a100000-0000-4000-8000-000000000005"
	const storeEpoch = "5a100000-0000-4000-8000-000000000006"
	now := time.Date(2026, time.July, 14, 23, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'tool-join-owner@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Tool Join Owner','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	seedExecutionBehavior(t, ctx, admin, tenantID, userID, "route_planner", now)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0xa5}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "tool-join", Pepper: bytes.Repeat([]byte{0xa6}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://tool-join/accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://tool-join/queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://tool-join/start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: "encrypted://tool-join/start", PayloadHash: "start"}, ConsumerName: "agent-worker", WorkerID: "agent-one", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://tool-join/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-join/run-attempt-started", Hash: "run-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-join/run-attempt-expired", Hash: "run-attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "parallel-join", JoinPolicy: "all", QuorumCount: 2, PlanResultHash: "parallel-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-join/run-attempt-completed", Hash: "run-attempt-completed"}, ToolRequests: []ToolRequest{
		{ToolName: "web_search", DescriptorSnapshotID: "web_search@v1", NormalizedInputRef: "encrypted://tool-join/input/one", RequestHash: "request-one", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 3, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-join/requested/one", Hash: "requested-one"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-join/execute/one", Hash: "execute-one"}},
		{ToolName: "github_create_issue", DescriptorSnapshotID: "github_create_issue@v1", NormalizedInputRef: "encrypted://tool-join/input/two", RequestHash: "request-two", EffectClass: "idempotent_write", EffectKey: "issue:tool-join", EffectScope: "tenant:github:tool-join", ProviderID: "github", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 55, CostUnits: 1, MaxAttempts: 3, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-join/requested/two", Hash: "requested-two"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-join/execute/two", Hash: "execute-two"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	claims := make([]ToolClaim, 2)
	for index, tool := range requested.ToolCalls {
		claims[index], err = store.ClaimTool(ctx, ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: []string{"encrypted://tool-join/execute/one", "encrypted://tool-join/execute/two"}[index], PayloadHash: []string{"execute-one", "execute-two"}[index]}, ConsumerName: "tool-worker", WorkerID: "tool-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: PayloadPointer{Ref: "encrypted://tool-join/started/" + string(rune('1'+index)), Hash: "started-" + string(rune('1'+index))}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-join/attempt-started/" + string(rune('1'+index)), Hash: "attempt-started-" + string(rune('1'+index))}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-join/attempt-expired/" + string(rune('1'+index)), Hash: "attempt-expired-" + string(rune('1'+index))}})
		if err != nil {
			t.Fatal(err)
		}
	}
	commands := make([]CompleteToolCommand, 2)
	for index, claim := range claims {
		suffix := string(rune('1' + index))
		commands[index] = CompleteToolCommand{Claim: claim, ExpectedToolVersion: claim.ToolCallVersion, TargetState: statemachine.ToolCallSucceeded, ResultHash: "result-" + suffix, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolCompletedEvent: PayloadPointer{Ref: "encrypted://tool-join/completed/" + suffix, Hash: "completed-" + suffix}, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-join/attempt-completed/" + suffix, Hash: "attempt-completed-" + suffix}, GroupJoinedEvent: PayloadPointer{Ref: "encrypted://tool-join/group-joined", Hash: "group-joined"}, RunResumeQueuedEvent: PayloadPointer{Ref: "encrypted://tool-join/resume-queued", Hash: "resume-queued"}, ResumeCommand: PayloadPointer{Ref: "encrypted://tool-join/resume", Hash: "resume"}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 4, ResumeMaxAttempts: 5}
	}
	results := make(chan CompletedTool, 2)
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for index, command := range commands {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var result CompletedTool
			var completeErr error
			if index == 0 {
				result, completeErr = store.CompleteReadOnlyTool(ctx, command)
			} else {
				result, completeErr = store.CompleteEffectTool(ctx, command, EffectCompletion{ExternalResourceRef: "github://issues/42"})
			}
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
		t.Fatalf("completion error: %v", completeErr)
	}
	completed, resumed := 0, 0
	var winner CompletedTool
	for result := range results {
		completed++
		if result.Resumed {
			resumed++
			winner = result
		}
	}
	if completed != 2 || resumed != 1 || winner.RunVersion != 5 {
		t.Fatalf("completed=%d resumed=%d winner=%#v", completed, resumed, winner)
	}
	var runStatus, groupContinuation, continuationStatus, resumeJobStatus, effectStatus, effectAttempt, effectProviderRequest, externalResource string
	var runVersion, groupVersion, continuations, resumeJobs, toolSucceeded, completedInboxes, succeededAttempts, succeededJobs, runEvents, toolEvents, groupEvents, outbox, effectVersion, effectFence int
	var groupJoined bool
	err = admin.QueryRow(ctx, `SELECT r.status,r.run_version,g.version,g.joined,g.continuation_id::text,c.status,j.status,e.status,e.version,e.execution_attempt_id::text,e.execution_fence,e.provider_request_id,e.external_resource_ref,(SELECT count(*) FROM agent.continuations WHERE tenant_id=$1 AND group_id=$3),(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND command_id=$4),(SELECT count(*) FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2 AND status='succeeded' AND tool_call_version=3 AND result_event_id IS NOT NULL),(SELECT count(*) FROM agent.inbox WHERE tenant_id=$1 AND consumer_name='tool-worker' AND status='completed'),(SELECT count(*) FROM agent.job_attempts WHERE tenant_id=$1 AND id=ANY($5::uuid[]) AND status='succeeded'),(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND id=ANY($6::uuid[]) AND status='succeeded'),(SELECT count(*) FROM agent.events WHERE aggregate_kind='run' AND aggregate_id=$2),(SELECT count(*) FROM agent.events WHERE aggregate_kind='tool_call' AND aggregate_id IN (SELECT tool_call_id FROM agent.parallel_group_members WHERE group_id=$3)),(SELECT count(*) FROM agent.events WHERE aggregate_kind='parallel_group' AND aggregate_id=$3),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1) FROM agent.runs r JOIN agent.parallel_groups g ON g.id=$3 JOIN agent.continuations c ON c.id=g.continuation_id JOIN agent.jobs j ON j.command_id=c.command_id JOIN agent.tool_effects e ON e.tool_call_id=$7 WHERE r.id=$2`, tenantID, runID, requested.GroupID, winner.ResumeCommandID, []string{claims[0].AttemptID, claims[1].AttemptID}, []string{claims[0].JobID, claims[1].JobID}, claims[1].ToolCallID).Scan(&runStatus, &runVersion, &groupVersion, &groupJoined, &groupContinuation, &continuationStatus, &resumeJobStatus, &effectStatus, &effectVersion, &effectAttempt, &effectFence, &effectProviderRequest, &externalResource, &continuations, &resumeJobs, &toolSucceeded, &completedInboxes, &succeededAttempts, &succeededJobs, &runEvents, &toolEvents, &groupEvents, &outbox)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || runVersion != 5 || groupVersion != 2 || !groupJoined || groupContinuation != winner.ContinuationID || continuationStatus != "committed" || resumeJobStatus != "pending" {
		t.Fatalf("run=%s/v%d group=v%d/%v/%s continuation=%s job=%s", runStatus, runVersion, groupVersion, groupJoined, groupContinuation, continuationStatus, resumeJobStatus)
	}
	if effectStatus != "confirmed" || effectVersion != 3 || effectAttempt != claims[1].AttemptID || effectFence != 1 || effectProviderRequest != claims[1].ProviderRequestID || externalResource != "github://issues/42" {
		t.Fatalf("effect=%s/v%d/%s/f%d/provider=%s resource=%s", effectStatus, effectVersion, effectAttempt, effectFence, effectProviderRequest, externalResource)
	}
	if continuations != 1 || resumeJobs != 1 || toolSucceeded != 2 || completedInboxes != 2 || succeededAttempts != 2 || succeededJobs != 2 || runEvents != 4 || toolEvents != 6 || groupEvents != 1 || outbox != 21 {
		t.Fatalf("continuations=%d resume_jobs=%d tools=%d inboxes=%d attempts=%d jobs=%d events=%d/%d/%d outbox=%d", continuations, resumeJobs, toolSucceeded, completedInboxes, succeededAttempts, succeededJobs, runEvents, toolEvents, groupEvents, outbox)
	}
}

func TestEffectOutcomeUnknownCanDeferThenEscalateToManualRepair(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "5a200000-0000-4000-8000-000000000001"
	const userID = "5a200000-0000-4000-8000-000000000002"
	const runID = "5a200000-0000-4000-8000-000000000003"
	const conversationID = "5a200000-0000-4000-8000-000000000004"
	const correlationID = "5a200000-0000-4000-8000-000000000005"
	const storeEpoch = "5a200000-0000-4000-8000-000000000006"
	now := time.Date(2026, time.July, 15, 1, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'tool-unknown-owner@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Tool Unknown Owner','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	seedExecutionBehavior(t, ctx, admin, tenantID, userID, "route_planner", now)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0xb5}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "tool-unknown", Pepper: bytes.Repeat([]byte{0xb6}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://tool-unknown/start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: "encrypted://tool-unknown/start", PayloadHash: "start"}, ConsumerName: "agent-worker", WorkerID: "agent-one", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://tool-unknown/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/run-attempt-started", Hash: "run-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-unknown/run-attempt-expired", Hash: "run-attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "unknown-join", JoinPolicy: "all", QuorumCount: 1, PlanResultHash: "unknown-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/run-attempt-completed", Hash: "run-attempt-completed"}, ToolRequests: []ToolRequest{{ToolName: "publish_career_evidence", DescriptorSnapshotID: "publish_career_evidence@v1", NormalizedInputRef: "encrypted://tool-unknown/input", RequestHash: "unknown-request", EffectClass: "reconcilable_write", EffectKey: "evidence-publish:unknown", EffectScope: "tenant:evidence:unknown", ProviderID: "career_evidence", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 3, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/requested", Hash: "requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-unknown/execute", Hash: "execute"}}}})
	if err != nil {
		t.Fatal(err)
	}
	tool := requested.ToolCalls[0]
	claim, err := store.ClaimTool(ctx, ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: "encrypted://tool-unknown/execute", PayloadHash: "execute"}, ConsumerName: "tool-worker", WorkerID: "tool-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/started", Hash: "started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/attempt-started", Hash: "attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-unknown/attempt-expired", Hash: "attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	reconcileAt := now.Add(5 * time.Minute)
	completed, err := store.CompleteEffectTool(ctx, CompleteToolCommand{Claim: claim, ExpectedToolVersion: claim.ToolCallVersion, TargetState: statemachine.ToolCallOutcomeUnknown, ResultHash: "unknown-result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolCompletedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/completed", Hash: "completed"}, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/attempt-completed", Hash: "attempt-completed"}, GroupJoinedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/group-joined", Hash: "group-joined"}, RunResumeQueuedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/resume-queued", Hash: "resume-queued"}, ResumeCommand: PayloadPointer{Ref: "encrypted://tool-unknown/resume", Hash: "resume"}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 4, ResumeMaxAttempts: 5}, EffectCompletion{ReconciliationDueAt: reconcileAt, ReconcileCommand: PayloadPointer{Ref: "encrypted://tool-unknown/reconcile", Hash: "reconcile"}, ReconcileQueueClass: "background", ReconcileResource: "tool-reconciliation", ReconcilePriority: 40, ReconcileCostUnits: 1, ReconcileAttempts: 8})
	if err != nil || completed.Status != statemachine.ToolCallOutcomeUnknown || completed.Resumed || completed.ReconcileCommandID == "" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	var runStatus, toolStatus, inboxStatus, attemptStatus, jobStatus, effectStatus, toolResultEvent, effectResultEvent, reconcileOutboxStatus, reconcileJobStatus string
	var runVersion, toolVersion, effectVersion, continuations, outbox, reconcileJobs int
	var groupJoined bool
	var actualReconcileAt, outboxAvailableAt, jobAvailableAt time.Time
	err = admin.QueryRow(ctx, `SELECT r.status,r.run_version,t.status,t.tool_call_version,t.result_event_id::text,i.status,a.status,j.status,e.status,e.version,e.result_event_id::text,e.reconciliation_due_at,g.joined,ro.status,ro.available_at,rj.status,rj.available_at,(SELECT count(*) FROM agent.continuations WHERE tenant_id=$1 AND group_id=$3),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1),(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND command_id=$7) FROM agent.runs r JOIN agent.tool_calls t ON t.run_id=r.id JOIN agent.inbox i ON i.id=$4 JOIN agent.job_attempts a ON a.id=$5 JOIN agent.jobs j ON j.id=$6 JOIN agent.tool_effects e ON e.tool_call_id=t.id JOIN agent.parallel_groups g ON g.id=$3 JOIN agent.outbox ro ON ro.tenant_id=$1 AND ro.command_id=$7 JOIN agent.jobs rj ON rj.tenant_id=$1 AND rj.command_id=$7 WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, requested.GroupID, claim.InboxID, claim.AttemptID, claim.JobID, completed.ReconcileCommandID).Scan(&runStatus, &runVersion, &toolStatus, &toolVersion, &toolResultEvent, &inboxStatus, &attemptStatus, &jobStatus, &effectStatus, &effectVersion, &effectResultEvent, &actualReconcileAt, &groupJoined, &reconcileOutboxStatus, &outboxAvailableAt, &reconcileJobStatus, &jobAvailableAt, &continuations, &outbox, &reconcileJobs)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "waiting_tool" || runVersion != 4 || toolStatus != "outcome_unknown" || toolVersion != 3 || toolResultEvent == "" || toolResultEvent != effectResultEvent || inboxStatus != "completed" || attemptStatus != "succeeded" || jobStatus != "succeeded" || effectStatus != "outcome_unknown" || effectVersion != 3 || !actualReconcileAt.Equal(reconcileAt) || groupJoined || reconcileOutboxStatus != "pending" || !outboxAvailableAt.Equal(reconcileAt) || reconcileJobStatus != "pending" || !jobAvailableAt.Equal(reconcileAt) || continuations != 0 || outbox != 13 || reconcileJobs != 1 {
		t.Fatalf("run=%s/v%d tool=%s/v%d events=%s/%s inbox=%s attempt=%s job=%s effect=%s/v%d reconcile=%s joined=%v reconcile_command=%s/%s/%s/%s continuations=%d outbox=%d jobs=%d", runStatus, runVersion, toolStatus, toolVersion, toolResultEvent, effectResultEvent, inboxStatus, attemptStatus, jobStatus, effectStatus, effectVersion, actualReconcileAt, groupJoined, reconcileOutboxStatus, outboxAvailableAt, reconcileJobStatus, jobAvailableAt, continuations, outbox, reconcileJobs)
	}

	now = reconcileAt
	reconcileCommand := ClaimReconciliationCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: completed.ReconcileCommandID, CommandType: "ReconcileToolEffect", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: "encrypted://tool-unknown/reconcile", PayloadHash: "reconcile"}, ConsumerName: "reconciliation-worker", WorkerID: "reconciliation-one", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/reconcile-attempt-started", Hash: "reconcile-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-unknown/reconcile-attempt-expired", Hash: "reconcile-attempt-expired"}}
	firstReconcile := competeForReconciliationClaim(t, ctx, store, reconcileCommand, 32)
	if firstReconcile.Fence != 1 || firstReconcile.ToolCallVersion != 3 || firstReconcile.EffectVersion != 3 || firstReconcile.EffectID != claim.EffectID || firstReconcile.ProviderRequestID != claim.ProviderRequestID {
		t.Fatalf("first reconciliation claim=%#v", firstReconcile)
	}
	now = now.Add(30 * time.Second)
	heartbeated, err := store.HeartbeatReconciliation(ctx, firstReconcile)
	if err != nil || !heartbeated.LeaseExpiresAt.After(firstReconcile.LeaseExpiresAt) {
		t.Fatalf("reconciliation heartbeat=%#v err=%v", heartbeated, err)
	}
	now = heartbeated.LeaseExpiresAt
	reclaimed := competeForReconciliationClaim(t, ctx, store, reconcileCommand, 32)
	if reclaimed.Fence != 2 || reclaimed.AttemptID == firstReconcile.AttemptID || reclaimed.ToolCallVersion != 3 || reclaimed.EffectVersion != 3 || reclaimed.EffectID != firstReconcile.EffectID {
		t.Fatalf("reclaimed reconciliation=%#v", reclaimed)
	}
	if _, err = store.HeartbeatReconciliation(ctx, heartbeated); !errors.Is(err, ErrExecutionRightConflict) {
		t.Fatalf("stale reconciliation heartbeat error=%v", err)
	}
	var durableToolStatus, durableEffectStatus, durableInboxStatus, oldReconcileStatus, newReconcileStatus, durableReconcileJobStatus string
	var durableToolVersion, durableEffectVersion, durableReconcileFence, reconcileAttemptCount, finalOutbox int
	err = admin.QueryRow(ctx, `SELECT t.status,t.tool_call_version,e.status,e.version,i.status,i.fence,oa.status,na.status,j.status,(SELECT count(*) FROM agent.job_attempts WHERE tenant_id=$1 AND command_id=$3),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1) FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tool_call_id=t.id JOIN agent.inbox i ON i.id=$4 JOIN agent.job_attempts oa ON oa.id=$5 JOIN agent.job_attempts na ON na.id=$6 JOIN agent.jobs j ON j.id=$7 WHERE t.tenant_id=$1 AND t.id=$2`, tenantID, tool.ToolCallID, completed.ReconcileCommandID, reclaimed.InboxID, firstReconcile.AttemptID, reclaimed.AttemptID, reclaimed.JobID).Scan(&durableToolStatus, &durableToolVersion, &durableEffectStatus, &durableEffectVersion, &durableInboxStatus, &durableReconcileFence, &oldReconcileStatus, &newReconcileStatus, &durableReconcileJobStatus, &reconcileAttemptCount, &finalOutbox)
	if err != nil {
		t.Fatal(err)
	}
	if durableToolStatus != "outcome_unknown" || durableToolVersion != 3 || durableEffectStatus != "outcome_unknown" || durableEffectVersion != 3 || durableInboxStatus != "running" || durableReconcileFence != 2 || oldReconcileStatus != "expired" || newReconcileStatus != "running" || durableReconcileJobStatus != "running" || reconcileAttemptCount != 2 || finalOutbox != 16 {
		t.Fatalf("tool=%s/v%d effect=%s/v%d inbox=%s/f%d attempts=%s/%s job=%s attempt_count=%d outbox=%d", durableToolStatus, durableToolVersion, durableEffectStatus, durableEffectVersion, durableInboxStatus, durableReconcileFence, oldReconcileStatus, newReconcileStatus, durableReconcileJobStatus, reconcileAttemptCount, finalOutbox)
	}
	nextReconcileAt := now.Add(5 * time.Minute)
	deferred := competeForReconciliationDeferral(t, ctx, store, DeferReconciliationCommand{Claim: reclaimed, ExpectedEffectVersion: reclaimed.EffectVersion, ResultHash: "reconciliation-still-unknown", NextDueAt: nextReconcileAt, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/reconcile-inconclusive", Hash: "reconcile-inconclusive"}, NextReconcileCommand: PayloadPointer{Ref: "encrypted://tool-unknown/reconcile-next", Hash: "reconcile-next"}, QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 8}, 32)
	if deferred.EffectVersion != 4 || deferred.NextCommandID == "" || deferred.NextJobID == "" || !deferred.DueAt.Equal(nextReconcileAt) {
		t.Fatalf("deferred=%#v", deferred)
	}
	var retryAggregateKind, retryAggregateID, retryOutboxStatus, retryJobStatus string
	var retryOutboxAt, retryJobAt time.Time
	err = admin.QueryRow(ctx, `SELECT o.aggregate_kind,o.aggregate_id::text,o.status,o.available_at,j.status,j.available_at FROM agent.outbox o JOIN agent.jobs j ON j.tenant_id=o.tenant_id AND j.command_id=o.command_id WHERE o.tenant_id=$1 AND o.command_id=$2`, tenantID, deferred.NextCommandID).Scan(&retryAggregateKind, &retryAggregateID, &retryOutboxStatus, &retryOutboxAt, &retryJobStatus, &retryJobAt)
	if err != nil {
		t.Fatal(err)
	}
	if retryAggregateKind != "tool_call" || retryAggregateID != tool.ToolCallID || retryOutboxStatus != "pending" || !retryOutboxAt.Equal(nextReconcileAt) || retryJobStatus != "pending" || !retryJobAt.Equal(nextReconcileAt) {
		t.Fatalf("retry target=%s/%s outbox=%s/%s job=%s/%s", retryAggregateKind, retryAggregateID, retryOutboxStatus, retryOutboxAt, retryJobStatus, retryJobAt)
	}
	if _, err = store.ClaimReconciliation(ctx, reconcileCommand); !errors.Is(err, ErrClaimCompleted) {
		t.Fatalf("completed reconciliation redelivery error=%v", err)
	}
	now = deferred.DueAt
	nextReconcileCommand := ClaimReconciliationCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: deferred.NextCommandID, CommandType: "ReconcileToolEffect", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: "encrypted://tool-unknown/reconcile-next", PayloadHash: "reconcile-next"}, ConsumerName: "reconciliation-worker", WorkerID: "reconciliation-two", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/reconcile-next-started", Hash: "reconcile-next-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-unknown/reconcile-next-expired", Hash: "reconcile-next-expired"}}
	retryClaim := competeForReconciliationClaim(t, ctx, store, nextReconcileCommand, 32)
	if retryClaim.EffectVersion != 4 || retryClaim.ToolCallVersion != 3 || !retryClaim.ReconciliationDueAt.Equal(nextReconcileAt) {
		t.Fatalf("retry claim=%#v", retryClaim)
	}
	var outboxBeforeEscalation int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.outbox WHERE tenant_id=$1`, tenantID).Scan(&outboxBeforeEscalation); err != nil {
		t.Fatal(err)
	}
	escalated := competeForReconciliationEscalation(t, ctx, store, EscalateReconciliationCommand{Claim: retryClaim, ExpectedEffectVersion: retryClaim.EffectVersion, ResultHash: "manual-review-required", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-unknown/manual-review-required", Hash: "manual-review-required"}}, 32)
	if escalated.ToolCallID != tool.ToolCallID || escalated.EffectID != retryClaim.EffectID || escalated.AttemptID != retryClaim.AttemptID || escalated.ToolCallVersion != 3 || escalated.EffectVersion != 4 {
		t.Fatalf("escalated=%#v", escalated)
	}
	if _, err = store.ClaimReconciliation(ctx, nextReconcileCommand); !errors.Is(err, ErrClaimCompleted) {
		t.Fatalf("escalated reconciliation redelivery error=%v", err)
	}
	var postEscalationRunStatus, postEscalationToolStatus, postEscalationEffectStatus, postEscalationInboxStatus, postEscalationAttemptStatus, postEscalationJobStatus, finalToolEvent, finalEffectEvent string
	var postEscalationRunVersion, postEscalationToolVersion, postEscalationEffectVersion, finalContinuations, postEscalationOutbox, pendingReconciliationJobs int
	var postEscalationJoined bool
	var postEscalationDue *time.Time
	err = admin.QueryRow(ctx, `SELECT r.status,r.run_version,t.status,t.tool_call_version,t.result_event_id::text,e.status,e.version,e.result_event_id::text,e.reconciliation_due_at,i.status,a.status,j.status,g.joined,(SELECT count(*) FROM agent.continuations WHERE tenant_id=$1 AND group_id=$3),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1),(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND resource_class='tool-reconciliation' AND status IN ('pending','running')) FROM agent.runs r JOIN agent.tool_calls t ON t.run_id=r.id JOIN agent.tool_effects e ON e.tool_call_id=t.id JOIN agent.inbox i ON i.id=$4 JOIN agent.job_attempts a ON a.id=$5 JOIN agent.jobs j ON j.id=$6 JOIN agent.parallel_groups g ON g.id=$3 WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, requested.GroupID, retryClaim.InboxID, retryClaim.AttemptID, retryClaim.JobID).Scan(&postEscalationRunStatus, &postEscalationRunVersion, &postEscalationToolStatus, &postEscalationToolVersion, &finalToolEvent, &postEscalationEffectStatus, &postEscalationEffectVersion, &finalEffectEvent, &postEscalationDue, &postEscalationInboxStatus, &postEscalationAttemptStatus, &postEscalationJobStatus, &postEscalationJoined, &finalContinuations, &postEscalationOutbox, &pendingReconciliationJobs)
	if err != nil {
		t.Fatal(err)
	}
	if postEscalationRunStatus != "waiting_tool" || postEscalationRunVersion != 4 || postEscalationToolStatus != "outcome_unknown" || postEscalationToolVersion != 3 || finalToolEvent != toolResultEvent || finalToolEvent != finalEffectEvent || postEscalationEffectStatus != "outcome_unknown" || postEscalationEffectVersion != 4 || postEscalationDue == nil || !postEscalationDue.Equal(nextReconcileAt) || postEscalationInboxStatus != "completed" || postEscalationAttemptStatus != "succeeded" || postEscalationJobStatus != "succeeded" || postEscalationJoined || finalContinuations != 0 || postEscalationOutbox != outboxBeforeEscalation+1 || pendingReconciliationJobs != 0 {
		t.Fatalf("run=%s/v%d tool=%s/v%d event=%s old=%s effect=%s/v%d/event=%s due=%v inbox=%s attempt=%s job=%s joined=%v continuations=%d outbox=%d/%d pending_reconcile=%d", postEscalationRunStatus, postEscalationRunVersion, postEscalationToolStatus, postEscalationToolVersion, finalToolEvent, toolResultEvent, postEscalationEffectStatus, postEscalationEffectVersion, finalEffectEvent, postEscalationDue, postEscalationInboxStatus, postEscalationAttemptStatus, postEscalationJobStatus, postEscalationJoined, finalContinuations, postEscalationOutbox, outboxBeforeEscalation, pendingReconciliationJobs)
	}
}

func competeForReconciliationClaim(t *testing.T, ctx context.Context, store RunStore, command ClaimReconciliationCommand, contenders int) ReconciliationClaim {
	t.Helper()
	var wait sync.WaitGroup
	winners := make(chan ReconciliationClaim, contenders)
	failures := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := store.ClaimReconciliation(ctx, command)
			if err != nil {
				failures <- err
				return
			}
			winners <- claim
		}()
	}
	wait.Wait()
	close(winners)
	close(failures)
	var winner ReconciliationClaim
	successes, busy := 0, 0
	for claim := range winners {
		winner = claim
		successes++
	}
	for err := range failures {
		if !errors.Is(err, ErrClaimBusy) {
			t.Fatalf("unexpected reconciliation claim error: %v", err)
		}
		busy++
	}
	if successes != 1 || busy != contenders-1 {
		t.Fatalf("reconciliation successes=%d busy=%d", successes, busy)
	}
	return winner
}

func competeForReconciliationCompletion(t *testing.T, ctx context.Context, store RunStore, command CompleteReconciliationCommand, contenders int) CompletedTool {
	t.Helper()
	var wait sync.WaitGroup
	winners := make(chan CompletedTool, contenders)
	failures := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			completed, err := store.CompleteReconciliation(ctx, command)
			if err != nil {
				failures <- err
				return
			}
			winners <- completed
		}()
	}
	wait.Wait()
	close(winners)
	close(failures)
	var winner CompletedTool
	successes, stale := 0, 0
	for completed := range winners {
		winner = completed
		successes++
	}
	for err := range failures {
		if !errors.Is(err, ErrExecutionRightConflict) {
			t.Fatalf("unexpected reconciliation completion error: %v", err)
		}
		stale++
	}
	if successes != 1 || stale != contenders-1 {
		t.Fatalf("reconciliation completion successes=%d stale=%d", successes, stale)
	}
	return winner
}

func competeForReconciliationDeferral(t *testing.T, ctx context.Context, store RunStore, command DeferReconciliationCommand, contenders int) DeferredReconciliation {
	t.Helper()
	var wait sync.WaitGroup
	winners := make(chan DeferredReconciliation, contenders)
	failures := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			deferred, err := store.DeferReconciliation(ctx, command)
			if err != nil {
				failures <- err
				return
			}
			winners <- deferred
		}()
	}
	wait.Wait()
	close(winners)
	close(failures)
	var winner DeferredReconciliation
	successes, stale := 0, 0
	for deferred := range winners {
		winner = deferred
		successes++
	}
	for err := range failures {
		if !errors.Is(err, ErrExecutionRightConflict) {
			t.Fatalf("unexpected reconciliation deferral error: %v", err)
		}
		stale++
	}
	if successes != 1 || stale != contenders-1 {
		t.Fatalf("reconciliation deferral successes=%d stale=%d", successes, stale)
	}
	return winner
}

func competeForReconciliationEscalation(t *testing.T, ctx context.Context, store RunStore, command EscalateReconciliationCommand, contenders int) EscalatedReconciliation {
	t.Helper()
	var wait sync.WaitGroup
	winners := make(chan EscalatedReconciliation, contenders)
	failures := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			escalated, err := store.EscalateReconciliation(ctx, command)
			if err != nil {
				failures <- err
				return
			}
			winners <- escalated
		}()
	}
	wait.Wait()
	close(winners)
	close(failures)
	var winner EscalatedReconciliation
	successes, stale := 0, 0
	for escalated := range winners {
		winner = escalated
		successes++
	}
	for err := range failures {
		if !errors.Is(err, ErrExecutionRightConflict) {
			t.Fatalf("unexpected reconciliation escalation error: %v", err)
		}
		stale++
	}
	if successes != 1 || stale != contenders-1 {
		t.Fatalf("reconciliation escalation successes=%d stale=%d", successes, stale)
	}
	return winner
}
