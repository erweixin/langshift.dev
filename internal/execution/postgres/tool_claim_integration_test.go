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

func TestToolClaimHeartbeatAndExpiredReclaimAreFenced(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "5f000000-0000-4000-8000-000000000001"
	const userID = "5f000000-0000-4000-8000-000000000002"
	const runID = "5f000000-0000-4000-8000-000000000003"
	const conversationID = "5f000000-0000-4000-8000-000000000004"
	const correlationID = "5f000000-0000-4000-8000-000000000005"
	const storeEpoch = "5f000000-0000-4000-8000-000000000006"
	current := time.Date(2026, time.July, 14, 22, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'tool-claim-owner@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Tool Claim Owner','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return current }}, IDKey: bytes.Repeat([]byte{0xa2}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return current }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "tool-worker-claim", Pepper: bytes.Repeat([]byte{0xa3}, 32)}, LeaseTTL: time.Minute}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: current.Add(time.Hour), ProfileSnapshotID: "route_planner@sha256:tool-claim", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://tool-claim/accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://tool-claim/queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://tool-claim/start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: "encrypted://tool-claim/start", PayloadHash: "start"}, ConsumerName: "agent-run-worker", WorkerID: "run-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-attempt-started", Hash: "run-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-attempt-expired", Hash: "run-attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "tool-claim-step", JoinPolicy: "all", QuorumCount: 1, PlanResultHash: "tool-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-attempt-completed", Hash: "run-attempt-completed"}, ToolRequests: []ToolRequest{{ToolName: "web_search", DescriptorSnapshotID: "web_search@sha256:v1", NormalizedInputRef: "encrypted://tool-claim/input", RequestHash: "tool-request", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 5, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-claim/requested", Hash: "requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-claim/execute", Hash: "execute"}}}})
	if err != nil {
		t.Fatal(err)
	}
	tool := requested.ToolCalls[0]
	claimCommand := ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: "encrypted://tool-claim/execute", PayloadHash: "execute"}, ConsumerName: "tool-worker", WorkerID: "tool-worker-one", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/started", Hash: "started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/attempt-started", Hash: "attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/attempt-expired", Hash: "attempt-expired"}}
	first := competeForToolClaim(t, ctx, store, claimCommand, 32)
	if first.Fence != 1 || first.ToolCallVersion != 2 || first.GroupID != requested.GroupID {
		t.Fatalf("unexpected first claim: %#v", first)
	}
	current = current.Add(30 * time.Second)
	heartbeated, err := store.HeartbeatTool(ctx, first)
	if err != nil || !heartbeated.LeaseExpiresAt.After(first.LeaseExpiresAt) {
		t.Fatalf("heartbeat=%#v err=%v", heartbeated, err)
	}
	current = heartbeated.LeaseExpiresAt
	reclaimed := competeForToolClaim(t, ctx, store, claimCommand, 32)
	if reclaimed.Fence != 2 || reclaimed.AttemptID == first.AttemptID || reclaimed.ToolCallVersion != 2 {
		t.Fatalf("unexpected reclaimed claim: %#v", reclaimed)
	}
	if _, err = store.HeartbeatTool(ctx, heartbeated); !errors.Is(err, ErrExecutionRightConflict) {
		t.Fatalf("stale heartbeat error=%v", err)
	}
	var toolStatus, inboxStatus, oldAttemptStatus, newAttemptStatus, jobStatus string
	var toolVersion, fence, toolEvents, oldAttemptEvents, newAttemptEvents, outbox int
	var activeAttempt string
	err = admin.QueryRow(ctx, `SELECT t.status,t.tool_call_version,t.current_fence,t.active_attempt_id::text,i.status,oa.status,na.status,j.status,(SELECT count(*) FROM agent.events WHERE aggregate_kind='tool_call' AND aggregate_id=$2),(SELECT count(*) FROM agent.events WHERE aggregate_kind='job_attempt' AND aggregate_id=$3),(SELECT count(*) FROM agent.events WHERE aggregate_kind='job_attempt' AND aggregate_id=$4),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1) FROM agent.tool_calls t JOIN agent.inbox i ON i.id=$5 JOIN agent.job_attempts oa ON oa.id=$3 JOIN agent.job_attempts na ON na.id=$4 JOIN agent.jobs j ON j.id=$6 WHERE t.id=$2`, tenantID, tool.ToolCallID, first.AttemptID, reclaimed.AttemptID, reclaimed.InboxID, reclaimed.JobID).Scan(&toolStatus, &toolVersion, &fence, &activeAttempt, &inboxStatus, &oldAttemptStatus, &newAttemptStatus, &jobStatus, &toolEvents, &oldAttemptEvents, &newAttemptEvents, &outbox)
	if err != nil {
		t.Fatal(err)
	}
	if toolStatus != "executing" || toolVersion != 2 || fence != 2 || activeAttempt != reclaimed.AttemptID || inboxStatus != "running" || oldAttemptStatus != "expired" || newAttemptStatus != "running" || jobStatus != "running" {
		t.Fatalf("tool=%s/v%d/f%d active=%s inbox=%s attempts=%s/%s job=%s", toolStatus, toolVersion, fence, activeAttempt, inboxStatus, oldAttemptStatus, newAttemptStatus, jobStatus)
	}
	if toolEvents != 2 || oldAttemptEvents != 2 || newAttemptEvents != 1 || outbox != 12 {
		t.Fatalf("events tool=%d attempts=%d/%d outbox=%d", toolEvents, oldAttemptEvents, newAttemptEvents, outbox)
	}
}

func competeForToolClaim(t *testing.T, ctx context.Context, store RunStore, command ClaimToolCommand, contenders int) ToolClaim {
	t.Helper()
	var wait sync.WaitGroup
	winners := make(chan ToolClaim, contenders)
	failures := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := store.ClaimTool(ctx, command)
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
	var winner ToolClaim
	successes, busy := 0, 0
	for claim := range winners {
		winner = claim
		successes++
	}
	for err := range failures {
		if !errors.Is(err, ErrClaimBusy) {
			t.Fatalf("unexpected claim error: %v", err)
		}
		busy++
	}
	if successes != 1 || busy != contenders-1 {
		t.Fatalf("successes=%d busy=%d", successes, busy)
	}
	return winner
}
