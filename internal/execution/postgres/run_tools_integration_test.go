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

func TestRequestToolsAtomicallyYieldsRunWithCompleteGroup(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "5e000000-0000-4000-8000-000000000001"
	const userID = "5e000000-0000-4000-8000-000000000002"
	const runID = "5e000000-0000-4000-8000-000000000003"
	const conversationID = "5e000000-0000-4000-8000-000000000004"
	const correlationID = "5e000000-0000-4000-8000-000000000005"
	const storeEpoch = "5e000000-0000-4000-8000-000000000006"
	now := time.Date(2026, time.July, 14, 21, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'tool-plan-owner@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Tool Plan Owner','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x92}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "tool-plan-run", Pepper: bytes.Repeat([]byte{0x93}, 32)}, LeaseTTL: 2 * time.Minute}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), ProfileSnapshotID: "route_planner@sha256:tools", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://tools/accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://tools/queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://tools/start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 8, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: "encrypted://tools/start", PayloadHash: "start"}, ConsumerName: "agent-run-worker", WorkerID: "worker-tools", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://tools/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tools/attempt-started", Hash: "attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tools/attempt-expired", Hash: "attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	request := RequestToolsCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, StepID: "research-sources", JoinPolicy: "all", QuorumCount: 2, PlanResultHash: "tool-plan-result", Actor: json.RawMessage(`{"kind":"service","id":"agent-run-worker"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tools/attempt-completed", Hash: "attempt-completed"}, ToolRequests: []ToolRequest{
		{ToolName: "web_search", DescriptorSnapshotID: "web_search@sha256:v1", NormalizedInputRef: "encrypted://tools/input/search", RequestHash: "search-request", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 4, RequestedEvent: PayloadPointer{Ref: "encrypted://tools/event/search", Hash: "search-event"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tools/command/search", Hash: "search-command"}},
		{ToolName: "workspace_read", DescriptorSnapshotID: "workspace_read@sha256:v1", NormalizedInputRef: "encrypted://tools/input/workspace", RequestHash: "workspace-request", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-sandbox", Priority: 55, CostUnits: 1, MaxAttempts: 3, RequestedEvent: PayloadPointer{Ref: "encrypted://tools/event/workspace", Hash: "workspace-event"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tools/command/workspace", Hash: "workspace-command"}},
	}}
	const contenders = 32
	var wait sync.WaitGroup
	results := make(chan ToolsRequested, contenders)
	errorsFound := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, requestErr := store.RequestTools(ctx, request)
			if requestErr != nil {
				errorsFound <- requestErr
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	var winner ToolsRequested
	var successes, stale int
	for result := range results {
		winner = result
		successes++
	}
	for requestErr := range errorsFound {
		if !errors.Is(requestErr, ErrExecutionRightConflict) {
			t.Fatalf("unexpected request error: %v", requestErr)
		}
		stale++
	}
	if successes != 1 || stale != contenders-1 || winner.RunVersion != 4 || len(winner.ToolCalls) != 2 {
		t.Fatalf("winner=%#v successes=%d stale=%d", winner, successes, stale)
	}
	var runStatus, inboxStatus, attemptStatus, startJobStatus, joinPolicy, groupKind, continuationKind string
	var runVersion, requiredCount, quorumCount, groups, members, toolCalls, pendingToolJobs, runEvents, toolEvents, attemptEvents, outbox int
	var activeCleared bool
	err = admin.QueryRow(ctx, `
		SELECT r.status,r.run_version,r.active_command_id IS NULL AND r.active_attempt_id IS NULL AND r.lease_token_hash IS NULL AND r.lease_expires_at IS NULL,
		       i.status,a.status,j.status,g.join_policy,g.required_count,g.quorum_count,g.group_kind,g.continuation_kind,
		       (SELECT count(*) FROM agent.parallel_groups WHERE tenant_id=$1 AND run_id=$2),
		       (SELECT count(*) FROM agent.parallel_group_members WHERE tenant_id=$1 AND group_id=$3),
		       (SELECT count(*) FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2 AND status='requested'),
		       (SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND status='pending' AND command_id IN (SELECT pending_command_id FROM agent.tool_calls WHERE run_id=$2)),
		       (SELECT count(*) FROM agent.events WHERE aggregate_kind='run' AND aggregate_id=$2),
		       (SELECT count(*) FROM agent.events WHERE aggregate_kind='tool_call' AND aggregate_id IN (SELECT tool_call_id FROM agent.parallel_group_members WHERE group_id=$3)),
		       (SELECT count(*) FROM agent.events WHERE aggregate_kind='job_attempt' AND aggregate_id=$4),
		       (SELECT count(*) FROM agent.outbox WHERE tenant_id=$1)
		FROM agent.runs r JOIN agent.inbox i ON i.id=$5 JOIN agent.job_attempts a ON a.id=$4 JOIN agent.jobs j ON j.id=$6 JOIN agent.parallel_groups g ON g.id=$3 WHERE r.id=$2`, tenantID, runID, winner.GroupID, claim.AttemptID, claim.InboxID, claim.JobID).Scan(&runStatus, &runVersion, &activeCleared, &inboxStatus, &attemptStatus, &startJobStatus, &joinPolicy, &requiredCount, &quorumCount, &groupKind, &continuationKind, &groups, &members, &toolCalls, &pendingToolJobs, &runEvents, &toolEvents, &attemptEvents, &outbox)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "waiting_tool" || runVersion != 4 || !activeCleared || inboxStatus != "completed" || attemptStatus != "succeeded" || startJobStatus != "succeeded" || joinPolicy != "all" || requiredCount != 2 || quorumCount != 2 || groupKind != "execution" || continuationKind != "resume" {
		t.Fatalf("run=%s/v%d cleared=%v inbox=%s attempt=%s job=%s group=%s/%d/%d/%s/%s", runStatus, runVersion, activeCleared, inboxStatus, attemptStatus, startJobStatus, joinPolicy, requiredCount, quorumCount, groupKind, continuationKind)
	}
	if groups != 1 || members != 2 || toolCalls != 2 || pendingToolJobs != 2 || runEvents != 3 || toolEvents != 2 || attemptEvents != 2 || outbox != 10 {
		t.Fatalf("groups=%d members=%d tools=%d jobs=%d run_events=%d tool_events=%d attempt_events=%d outbox=%d", groups, members, toolCalls, pendingToolJobs, runEvents, toolEvents, attemptEvents, outbox)
	}
}
