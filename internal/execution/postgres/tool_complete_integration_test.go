//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
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
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0xa5}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "tool-join", Pepper: bytes.Repeat([]byte{0xa6}, 32)}, LeaseTTL: 2 * time.Minute}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), ProfileSnapshotID: "route_planner@sha256:tool-join", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://tool-join/accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://tool-join/queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://tool-join/start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: "encrypted://tool-join/start", PayloadHash: "start"}, ConsumerName: "agent-worker", WorkerID: "agent-one", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://tool-join/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-join/run-attempt-started", Hash: "run-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-join/run-attempt-expired", Hash: "run-attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "parallel-join", JoinPolicy: "all", QuorumCount: 2, PlanResultHash: "parallel-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-join/run-attempt-completed", Hash: "run-attempt-completed"}, ToolRequests: []ToolRequest{
		{ToolName: "web_search", DescriptorSnapshotID: "web_search@v1", NormalizedInputRef: "encrypted://tool-join/input/one", RequestHash: "request-one", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 3, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-join/requested/one", Hash: "requested-one"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-join/execute/one", Hash: "execute-one"}},
		{ToolName: "workspace_read", DescriptorSnapshotID: "workspace_read@v1", NormalizedInputRef: "encrypted://tool-join/input/two", RequestHash: "request-two", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-sandbox", Priority: 55, CostUnits: 1, MaxAttempts: 3, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-join/requested/two", Hash: "requested-two"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-join/execute/two", Hash: "execute-two"}},
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
	for _, command := range commands {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, completeErr := store.CompleteReadOnlyTool(ctx, command)
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
	var runStatus, groupContinuation, continuationStatus, resumeJobStatus string
	var runVersion, groupVersion, continuations, resumeJobs, toolSucceeded, completedInboxes, succeededAttempts, succeededJobs, runEvents, toolEvents, groupEvents, outbox int
	var groupJoined bool
	err = admin.QueryRow(ctx, `SELECT r.status,r.run_version,g.version,g.joined,g.continuation_id::text,c.status,j.status,(SELECT count(*) FROM agent.continuations WHERE tenant_id=$1 AND group_id=$3),(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND command_id=$4),(SELECT count(*) FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2 AND status='succeeded' AND tool_call_version=3 AND result_event_id IS NOT NULL),(SELECT count(*) FROM agent.inbox WHERE tenant_id=$1 AND consumer_name='tool-worker' AND status='completed'),(SELECT count(*) FROM agent.job_attempts WHERE tenant_id=$1 AND id=ANY($5::uuid[]) AND status='succeeded'),(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND id=ANY($6::uuid[]) AND status='succeeded'),(SELECT count(*) FROM agent.events WHERE aggregate_kind='run' AND aggregate_id=$2),(SELECT count(*) FROM agent.events WHERE aggregate_kind='tool_call' AND aggregate_id IN (SELECT tool_call_id FROM agent.parallel_group_members WHERE group_id=$3)),(SELECT count(*) FROM agent.events WHERE aggregate_kind='parallel_group' AND aggregate_id=$3),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1) FROM agent.runs r JOIN agent.parallel_groups g ON g.id=$3 JOIN agent.continuations c ON c.id=g.continuation_id JOIN agent.jobs j ON j.command_id=c.command_id WHERE r.id=$2`, tenantID, runID, requested.GroupID, winner.ResumeCommandID, []string{claims[0].AttemptID, claims[1].AttemptID}, []string{claims[0].JobID, claims[1].JobID}).Scan(&runStatus, &runVersion, &groupVersion, &groupJoined, &groupContinuation, &continuationStatus, &resumeJobStatus, &continuations, &resumeJobs, &toolSucceeded, &completedInboxes, &succeededAttempts, &succeededJobs, &runEvents, &toolEvents, &groupEvents, &outbox)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "queued" || runVersion != 5 || groupVersion != 2 || !groupJoined || groupContinuation != winner.ContinuationID || continuationStatus != "committed" || resumeJobStatus != "pending" {
		t.Fatalf("run=%s/v%d group=v%d/%v/%s continuation=%s job=%s", runStatus, runVersion, groupVersion, groupJoined, groupContinuation, continuationStatus, resumeJobStatus)
	}
	if continuations != 1 || resumeJobs != 1 || toolSucceeded != 2 || completedInboxes != 2 || succeededAttempts != 2 || succeededJobs != 2 || runEvents != 4 || toolEvents != 6 || groupEvents != 1 || outbox != 21 {
		t.Fatalf("continuations=%d resume_jobs=%d tools=%d inboxes=%d attempts=%d jobs=%d events=%d/%d/%d outbox=%d", continuations, resumeJobs, toolSucceeded, completedInboxes, succeededAttempts, succeededJobs, runEvents, toolEvents, groupEvents, outbox)
	}
}
