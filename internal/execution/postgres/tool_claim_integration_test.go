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
	const membershipID = "5f000000-0000-4000-8000-000000000007"
	current := time.Date(2026, time.July, 14, 22, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'tool-claim-owner@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Tool Claim Owner','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$4)`, membershipID, tenantID, userID, current); err != nil {
		t.Fatal(err)
	}
	seedExecutionBehavior(t, ctx, admin, tenantID, userID, "route_planner", current)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return current }}, IDKey: bytes.Repeat([]byte{0xa2}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return current }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "tool-worker-claim", Pepper: bytes.Repeat([]byte{0xa3}, 32)}, LeaseTTL: time.Minute, Behavior: integrationBehaviorResolver}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: current.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://tool-claim/accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://tool-claim/queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://tool-claim/start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: "encrypted://tool-claim/start", PayloadHash: "start"}, ConsumerName: "agent-run-worker", WorkerID: "run-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-attempt-started", Hash: "run-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-attempt-expired", Hash: "run-attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "tool-claim-step", JoinPolicy: "all", QuorumCount: 2, PlanResultHash: "tool-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-claim/run-attempt-completed", Hash: "run-attempt-completed"}, ToolRequests: []ToolRequest{
		{ToolName: "github_create_issue", DescriptorSnapshotID: "github_create_issue@sha256:v1", NormalizedInputRef: "encrypted://tool-claim/input", RequestHash: "tool-request", EffectClass: "idempotent_write", EffectKey: "issue:claim-test", EffectScope: "tenant:github:claim-test", ProviderID: "github", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 5, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-claim/requested", Hash: "requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-claim/execute", Hash: "execute"}},
		{ToolName: "publish_career_evidence", DescriptorSnapshotID: "publish_career_evidence@sha256:v1", NormalizedInputRef: "encrypted://tool-claim/reconcile-input", RequestHash: "reconcile-tool-request", EffectClass: "reconcilable_write", EffectKey: "evidence-publish:claim-test", EffectScope: "tenant:evidence:claim-test", ProviderID: "career_evidence", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 5, RequestedEvent: PayloadPointer{Ref: "encrypted://tool-claim/reconcile-requested", Hash: "reconcile-requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://tool-claim/reconcile-execute", Hash: "reconcile-execute"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	tool := requested.ToolCalls[0]
	claimCommand := ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: "encrypted://tool-claim/execute", PayloadHash: "execute"}, ConsumerName: "tool-worker", WorkerID: "tool-worker-one", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/started", Hash: "started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/attempt-started", Hash: "attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/attempt-expired", Hash: "attempt-expired"}}
	wrongBinding := ToolBinding{ToolName: "substituted_tool", DescriptorSnapshotID: "github_create_issue@sha256:v1", NormalizedInputRef: "encrypted://tool-claim/input", RequestHash: "tool-request", EffectClass: "idempotent_write", EffectKey: "issue:claim-test", EffectScope: "tenant:github:claim-test", ProviderID: "github"}
	claimCommand.ExpectedBinding = &wrongBinding
	if _, err = store.ClaimTool(ctx, claimCommand); !errors.Is(err, ErrToolNotClaimable) {
		t.Fatalf("substituted command binding acquired execution right: %v", err)
	}
	expectedBinding := ToolBinding{ToolName: "github_create_issue", DescriptorSnapshotID: "github_create_issue@sha256:v1", NormalizedInputRef: "encrypted://tool-claim/input", RequestHash: "tool-request", EffectClass: "idempotent_write", EffectKey: "issue:claim-test", EffectScope: "tenant:github:claim-test", ProviderID: "github"}
	claimCommand.ExpectedBinding = &expectedBinding
	claimCommand.ExpectedPermissionSnapshot = "membership:substituted:v99:role:owner"
	if _, err = store.ClaimTool(ctx, claimCommand); !errors.Is(err, ErrToolNotClaimable) {
		t.Fatalf("substituted permission snapshot acquired execution right: %v", err)
	}
	claimCommand.ExpectedPermissionSnapshot = "membership:" + membershipID + ":v1:role:member"
	first := competeForToolClaim(t, ctx, store, claimCommand, 32)
	if first.Fence != 1 || first.ToolCallVersion != 2 || first.GroupID != requested.GroupID || first.EffectClass != "idempotent_write" || first.EffectID == "" || first.ProviderRequestID != first.EffectID || first.Binding != expectedBinding {
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
	if _, err = admin.Exec(ctx, `UPDATE identity.memberships SET version=2,status='left',deactivated_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, current, tenantID, membershipID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimTool(ctx, claimCommand); !errors.Is(err, ErrToolNotClaimable) {
		t.Fatalf("revoked membership reclaimed tool execution right: %v", err)
	}

	tenantIDs, err := store.ListExpiredEffectTenantIDs(ctx, storeEpoch, "", 10, 0, 1)
	if err != nil || len(tenantIDs) != 1 || tenantIDs[0] != tenantID {
		t.Fatalf("expired effect tenants=%v error=%v", tenantIDs, err)
	}
	candidates, err := store.ListExpiredEffectCandidates(ctx, tenantID, storeEpoch, "", 10)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("expired effect candidates=%#v error=%v", candidates, err)
	}
	var manualCandidate, automaticCandidate ExpiredToolEffectCandidate
	for _, candidate := range candidates {
		switch candidate.EffectClass {
		case "idempotent_write":
			manualCandidate = candidate
		case "reconcilable_write":
			automaticCandidate = candidate
		}
	}
	if manualCandidate.ToolCallID != tool.ToolCallID || manualCandidate.EffectID != reclaimed.EffectID || manualCandidate.AttemptID != reclaimed.AttemptID || automaticCandidate.ToolCallID != reconcileTool.ToolCallID || automaticCandidate.EffectID != reconcileClaim.EffectID || automaticCandidate.AttemptID != reconcileClaim.AttemptID {
		t.Fatalf("manual=%#v automatic=%#v", manualCandidate, automaticCandidate)
	}
	manualDue := current.Add(time.Minute)
	manualSweep := competeForEffectSweep(t, ctx, store, SweepExpiredToolEffectCommand{Candidate: manualCandidate, ResultHash: "idempotent-worker-lease-expired-manual-review", Actor: json.RawMessage(`{"kind":"service","name":"tool-effect-sweeper"}`), CorrelationID: correlationID, OutcomeUnknownEvent: PayloadPointer{Ref: "encrypted://tool-claim/manual-unknown", Hash: "manual-unknown"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/manual-attempt-expired", Hash: "manual-attempt-expired"}, Reconciliation: EffectCompletion{ReconciliationDueAt: manualDue}}, 32)
	if manualSweep.ReconcileCommandID != "" || manualSweep.ToolVersion != reclaimed.ToolCallVersion+1 || manualSweep.EffectVersion != manualCandidate.EffectVersion+1 {
		t.Fatalf("manual sweep=%#v", manualSweep)
	}
	var manualToolStatus, manualEffectStatus, manualInboxStatus, manualAttemptStatus, manualJobStatus string
	var manualReconciliationJobs int
	err = admin.QueryRow(ctx, `SELECT t.status,e.status,i.status,a.status,j.status,(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND resource_class='tool-reconciliation') FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tool_call_id=t.id JOIN agent.inbox i ON i.id=$3 JOIN agent.job_attempts a ON a.id=$4 JOIN agent.jobs j ON j.id=$5 WHERE t.tenant_id=$1 AND t.id=$2`, tenantID, tool.ToolCallID, reclaimed.InboxID, reclaimed.AttemptID, reclaimed.JobID).Scan(&manualToolStatus, &manualEffectStatus, &manualInboxStatus, &manualAttemptStatus, &manualJobStatus, &manualReconciliationJobs)
	if err != nil {
		t.Fatal(err)
	}
	if manualToolStatus != "outcome_unknown" || manualEffectStatus != "outcome_unknown" || manualInboxStatus != "completed" || manualAttemptStatus != "expired" || manualJobStatus != "succeeded" || manualReconciliationJobs != 0 {
		t.Fatalf("manual route tool=%s effect=%s inbox=%s attempt=%s job=%s reconciliation_jobs=%d", manualToolStatus, manualEffectStatus, manualInboxStatus, manualAttemptStatus, manualJobStatus, manualReconciliationJobs)
	}
	reconcileDue := current.Add(time.Minute)
	sweepCommand := SweepExpiredToolEffectCommand{Candidate: automaticCandidate, ResultHash: "worker-lease-expired-outcome-unknown", Actor: json.RawMessage(`{"kind":"service","name":"tool-effect-sweeper"}`), CorrelationID: correlationID, OutcomeUnknownEvent: PayloadPointer{Ref: "encrypted://tool-claim/swept-unknown", Hash: "swept-unknown"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/swept-attempt-expired", Hash: "swept-attempt-expired"}, Reconciliation: EffectCompletion{ReconciliationDueAt: reconcileDue, ReconcileCommand: PayloadPointer{Ref: "encrypted://tool-claim/swept-reconcile", Hash: "swept-reconcile"}, ReconcileQueueClass: "background", ReconcileResource: "tool-reconciliation", ReconcilePriority: 40, ReconcileCostUnits: 1, ReconcileAttempts: 8}}
	swept := competeForEffectSweep(t, ctx, store, sweepCommand, 32)
	if swept.ToolVersion != reconcileClaim.ToolCallVersion+1 || swept.EffectVersion != automaticCandidate.EffectVersion+1 || swept.ReconcileCommandID == "" {
		t.Fatalf("swept=%#v", swept)
	}
	lateCompletion := CompleteToolCommand{Claim: reconcileClaim, ExpectedToolVersion: reconcileClaim.ToolCallVersion, TargetState: statemachine.ToolCallOutcomeUnknown, ResultHash: "late-worker-unknown", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolCompletedEvent: PayloadPointer{Ref: "encrypted://tool-claim/late-unknown", Hash: "late-unknown"}, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://tool-claim/late-attempt", Hash: "late-attempt"}, GroupJoinedEvent: PayloadPointer{Ref: "encrypted://tool-claim/late-group", Hash: "late-group"}, RunResumeQueuedEvent: PayloadPointer{Ref: "encrypted://tool-claim/late-run", Hash: "late-run"}, ResumeCommand: PayloadPointer{Ref: "encrypted://tool-claim/late-resume", Hash: "late-resume"}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
	if _, err = store.CompleteEffectTool(ctx, lateCompletion, sweepCommand.Reconciliation); !errors.Is(err, ErrExecutionRightConflict) {
		t.Fatalf("late worker completion error=%v", err)
	}
	var sweptToolStatus, sweptEffectStatus, sweptInboxStatus, sweptAttemptStatus, sweptJobStatus string
	var sweptToolVersion, sweptEffectVersion, unknownEvents, reconcileJobs int
	var cleared bool
	err = admin.QueryRow(ctx, `SELECT t.status,t.tool_call_version,t.active_command_id IS NULL AND t.active_attempt_id IS NULL AND t.lease_token_hash IS NULL AND t.lease_expires_at IS NULL,e.status,e.version,i.status,a.status,j.status,(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$2 AND event_type='ToolCallOutcomeUnknown'),(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND command_id=$3 AND status='pending') FROM agent.tool_calls t JOIN agent.tool_effects e ON e.tool_call_id=t.id JOIN agent.inbox i ON i.id=$4 JOIN agent.job_attempts a ON a.id=$5 JOIN agent.jobs j ON j.id=$6 WHERE t.id=$2`, tenantID, reconcileTool.ToolCallID, swept.ReconcileCommandID, reconcileClaim.InboxID, reconcileClaim.AttemptID, reconcileClaim.JobID).Scan(&sweptToolStatus, &sweptToolVersion, &cleared, &sweptEffectStatus, &sweptEffectVersion, &sweptInboxStatus, &sweptAttemptStatus, &sweptJobStatus, &unknownEvents, &reconcileJobs)
	if err != nil {
		t.Fatal(err)
	}
	if sweptToolStatus != "outcome_unknown" || sweptToolVersion != int(swept.ToolVersion) || !cleared || sweptEffectStatus != "outcome_unknown" || sweptEffectVersion != int(swept.EffectVersion) || sweptInboxStatus != "completed" || sweptAttemptStatus != "expired" || sweptJobStatus != "succeeded" || unknownEvents != 1 || reconcileJobs != 1 {
		t.Fatalf("swept tool=%s/v%d cleared=%v effect=%s/v%d inbox=%s attempt=%s job=%s events=%d reconcile_jobs=%d", sweptToolStatus, sweptToolVersion, cleared, sweptEffectStatus, sweptEffectVersion, sweptInboxStatus, sweptAttemptStatus, sweptJobStatus, unknownEvents, reconcileJobs)
	}
	current = reconcileDue
	reconciliationClaim, err := store.ClaimReconciliation(ctx, ClaimReconciliationCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: swept.ReconcileCommandID, CommandType: "ReconcileToolEffect", AggregateKind: "tool_call", AggregateID: reconcileTool.ToolCallID, PayloadRef: "encrypted://tool-claim/swept-reconcile", PayloadHash: "swept-reconcile"}, ConsumerName: "reconciliation-worker", WorkerID: "reconciliation-after-sweep", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://tool-claim/swept-reconcile-started", Hash: "swept-reconcile-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://tool-claim/swept-reconcile-expired", Hash: "swept-reconcile-expired"}})
	if err != nil || reconciliationClaim.EffectID != swept.EffectID || reconciliationClaim.ProviderRequestID != reconcileClaim.ProviderRequestID || reconciliationClaim.Fence != 1 {
		t.Fatalf("reconciliation after sweep=%#v error=%v", reconciliationClaim, err)
	}
}

func competeForEffectSweep(t *testing.T, ctx context.Context, store RunStore, command SweepExpiredToolEffectCommand, contenders int) SweptToolEffect {
	t.Helper()
	winners := make(chan SweptToolEffect, contenders)
	failures := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.SweepExpiredToolEffect(ctx, command)
			if err != nil {
				failures <- err
				return
			}
			winners <- result
		}()
	}
	wait.Wait()
	close(winners)
	close(failures)
	var winner SweptToolEffect
	successes, stale := 0, 0
	for result := range winners {
		winner = result
		successes++
	}
	for err := range failures {
		if !errors.Is(err, ErrToolEffectNotSweepable) {
			t.Fatalf("unexpected effect sweep error=%v", err)
		}
		stale++
	}
	if successes != 1 || stale != contenders-1 {
		t.Fatalf("effect sweep successes=%d stale=%d", successes, stale)
	}
	return winner
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
