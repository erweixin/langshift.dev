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
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "tool-claim-step", JoinPolicy: "all", QuorumCount: 2, PlanResultHash: "tool-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-attempt-completed", Hash: "run-attempt-completed"}, ToolRequests: []ToolRequest{
		{ToolName: "github_create_issue", DescriptorSnapshotID: "github_create_issue@sha256:v1", NormalizedInputRef: "encrypted://tool-claim/input", RequestHash: "tool-request", EffectClass: "idempotent_write", EffectKey: "issue:claim-test", EffectScope: "tenant:github:claim-test", ProviderID: "github", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 5, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-claim/requested", Hash: "requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-claim/execute", Hash: "execute"}},
		{ToolName: "stripe_create_refund", DescriptorSnapshotID: "stripe_create_refund@sha256:v1", NormalizedInputRef: "encrypted://tool-claim/reconcile-input", RequestHash: "reconcile-tool-request", EffectClass: "reconcilable_write", EffectKey: "refund:claim-test", EffectScope: "tenant:stripe:claim-test", ProviderID: "stripe", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 5, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-claim/reconcile-requested", Hash: "reconcile-requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-claim/reconcile-execute", Hash: "reconcile-execute"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	tool := requested.ToolCalls[0]
	claimCommand := ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: "encrypted://tool-claim/execute", PayloadHash: "execute"}, ConsumerName: "tool-worker", WorkerID: "tool-worker-one", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/started", Hash: "started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/attempt-started", Hash: "attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/attempt-expired", Hash: "attempt-expired"}}
	first := competeForToolClaim(t, ctx, store, claimCommand, 32)
	if first.Fence != 1 || first.ToolCallVersion != 2 || first.GroupID != requested.GroupID || first.EffectClass != "idempotent_write" || first.EffectID == "" || first.ProviderRequestID != first.EffectID {
		t.Fatalf("unexpected first claim: %#v", first)
	}
	current = current.Add(30 * time.Second)
	heartbeated, err := store.HeartbeatTool(ctx, first)
	if err != nil || !heartbeated.LeaseExpiresAt.After(first.LeaseExpiresAt) {
		t.Fatalf("heartbeat=%#v err=%v", heartbeated, err)
	}
	current = heartbeated.LeaseExpiresAt
	reclaimed := competeForToolClaim(t, ctx, store, claimCommand, 32)
	if reclaimed.Fence != 2 || reclaimed.AttemptID == first.AttemptID || reclaimed.ToolCallVersion != 2 || reclaimed.EffectID != first.EffectID || reclaimed.ProviderRequestID != first.ProviderRequestID {
		t.Fatalf("unexpected reclaimed claim: %#v", reclaimed)
	}
	if _, err = store.HeartbeatTool(ctx, heartbeated); !errors.Is(err, ErrExecutionRightConflict) {
		t.Fatalf("stale heartbeat error=%v", err)
	}
	var toolStatus, inboxStatus, oldAttemptStatus, newAttemptStatus, jobStatus, effectStatus, effectAttempt, providerRequest string
	var toolVersion, fence, effectVersion, effectFence, toolEvents, oldAttemptEvents, newAttemptEvents, outbox int
	var activeAttempt string
	err = admin.QueryRow(ctx, `SELECT t.status,t.tool_call_version,t.current_fence,t.active_attempt_id::text,i.status,oa.status,na.status,j.status,e.status,e.version,e.execution_attempt_id::text,e.execution_fence,e.provider_request_id,(SELECT count(*) FROM agent.events WHERE aggregate_kind='tool_call' AND aggregate_id=$2),(SELECT count(*) FROM agent.events WHERE aggregate_kind='job_attempt' AND aggregate_id=$3),(SELECT count(*) FROM agent.events WHERE aggregate_kind='job_attempt' AND aggregate_id=$4),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1) FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tool_call_id=t.id JOIN agent.inbox i ON i.id=$5 JOIN agent.job_attempts oa ON oa.id=$3 JOIN agent.job_attempts na ON na.id=$4 JOIN agent.jobs j ON j.id=$6 WHERE t.id=$2`, tenantID, tool.ToolCallID, first.AttemptID, reclaimed.AttemptID, reclaimed.InboxID, reclaimed.JobID).Scan(&toolStatus, &toolVersion, &fence, &activeAttempt, &inboxStatus, &oldAttemptStatus, &newAttemptStatus, &jobStatus, &effectStatus, &effectVersion, &effectAttempt, &effectFence, &providerRequest, &toolEvents, &oldAttemptEvents, &newAttemptEvents, &outbox)
	if err != nil {
		t.Fatal(err)
	}
	if toolStatus != "executing" || toolVersion != 2 || fence != 2 || activeAttempt != reclaimed.AttemptID || inboxStatus != "running" || oldAttemptStatus != "expired" || newAttemptStatus != "running" || jobStatus != "running" || effectStatus != "executing" || effectVersion != 3 || effectAttempt != reclaimed.AttemptID || effectFence != 2 || providerRequest != first.ProviderRequestID {
		t.Fatalf("tool=%s/v%d/f%d active=%s inbox=%s attempts=%s/%s job=%s effect=%s/v%d/%s/f%d/provider=%s", toolStatus, toolVersion, fence, activeAttempt, inboxStatus, oldAttemptStatus, newAttemptStatus, jobStatus, effectStatus, effectVersion, effectAttempt, effectFence, providerRequest)
	}
	if toolEvents != 2 || oldAttemptEvents != 2 || newAttemptEvents != 1 || outbox != 14 {
		t.Fatalf("events tool=%d attempts=%d/%d outbox=%d", toolEvents, oldAttemptEvents, newAttemptEvents, outbox)
	}

	reconcileTool := requested.ToolCalls[1]
	reconcileCommand := ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: reconcileTool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: reconcileTool.ToolCallID, PayloadRef: "encrypted://tool-claim/reconcile-execute", PayloadHash: "reconcile-execute"}, ConsumerName: "tool-worker", WorkerID: "tool-worker-two", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/reconcile-started", Hash: "reconcile-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/reconcile-attempt-started", Hash: "reconcile-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/reconcile-attempt-expired", Hash: "reconcile-attempt-expired"}}
	reconcileClaim, err := store.ClaimTool(ctx, reconcileCommand)
	if err != nil || reconcileClaim.EffectClass != "reconcilable_write" || reconcileClaim.ProviderRequestID != reconcileClaim.EffectID {
		t.Fatalf("reconcile claim=%#v err=%v", reconcileClaim, err)
	}
	current = reconcileClaim.LeaseExpiresAt
	if _, err = store.ClaimTool(ctx, reconcileCommand); !errors.Is(err, ErrToolEffectNeedsReconciliation) {
		t.Fatalf("expired reconcilable claim error=%v", err)
	}
	var reconcileToolStatus, reconcileInboxStatus, reconcileAttemptStatus, reconcileEffectStatus, reconcileEffectAttempt, reconcileProviderRequest string
	var reconcileFence, reconcileEffectVersion, reconcileEffectFence, reconcileAttempts, reconcileAttemptEvents int
	err = admin.QueryRow(ctx, `SELECT t.status,t.current_fence,i.status,a.status,e.status,e.version,e.execution_attempt_id::text,e.execution_fence,e.provider_request_id,(SELECT count(*) FROM agent.job_attempts WHERE tenant_id=$1 AND command_id=$3),(SELECT count(*) FROM agent.events WHERE aggregate_kind='job_attempt' AND aggregate_id=$4) FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tool_call_id=t.id JOIN agent.inbox i ON i.tenant_id=t.tenant_id AND i.consumer_name='tool-worker' AND i.command_id=$3 JOIN agent.job_attempts a ON a.id=$4 WHERE t.tenant_id=$1 AND t.id=$2`, tenantID, reconcileTool.ToolCallID, reconcileTool.CommandID, reconcileClaim.AttemptID).Scan(&reconcileToolStatus, &reconcileFence, &reconcileInboxStatus, &reconcileAttemptStatus, &reconcileEffectStatus, &reconcileEffectVersion, &reconcileEffectAttempt, &reconcileEffectFence, &reconcileProviderRequest, &reconcileAttempts, &reconcileAttemptEvents)
	if err != nil {
		t.Fatal(err)
	}
	if reconcileToolStatus != "executing" || reconcileFence != 1 || reconcileInboxStatus != "running" || reconcileAttemptStatus != "running" || reconcileEffectStatus != "executing" || reconcileEffectVersion != 2 || reconcileEffectAttempt != reconcileClaim.AttemptID || reconcileEffectFence != 1 || reconcileProviderRequest != reconcileClaim.ProviderRequestID || reconcileAttempts != 1 || reconcileAttemptEvents != 1 {
		t.Fatalf("reconcilable tool=%s/f%d inbox=%s attempt=%s effect=%s/v%d/%s/f%d/provider=%s attempts=%d events=%d", reconcileToolStatus, reconcileFence, reconcileInboxStatus, reconcileAttemptStatus, reconcileEffectStatus, reconcileEffectVersion, reconcileEffectAttempt, reconcileEffectFence, reconcileProviderRequest, reconcileAttempts, reconcileAttemptEvents)
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
