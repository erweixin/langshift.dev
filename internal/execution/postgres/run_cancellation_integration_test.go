//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestRunCancellationBarrierIsEventBackedAndFencesWorkers(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()

	now := time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC)
	const storeEpoch = "ce000000-0000-4000-8000-000000000001"
	store := RunStore{
		Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }},
		IDKey: bytes.Repeat([]byte{0xc1}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now },
		Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "run-cancellation-test", Pepper: bytes.Repeat([]byte{0xc2}, 32)}, LeaseTTL: 2 * time.Minute,
		Behavior: integrationBehaviorResolver,
	}

	t.Run("queued run settles atomically and replays exactly", func(t *testing.T) {
		const tenantID = "ce100000-0000-4000-8000-000000000001"
		const userID = "ce100000-0000-4000-8000-000000000002"
		const runID = "ce100000-0000-4000-8000-000000000003"
		const conversationID = "ce100000-0000-4000-8000-000000000004"
		const correlationID = "ce100000-0000-4000-8000-000000000005"
		const cancellationID = "ce100000-0000-4000-8000-000000000006"
		seedCancellationIdentity(t, ctx, admin, tenantID, userID, "queued-cancel@example.invalid", now)
		accepted := acceptCancellationRun(t, ctx, store, runID, tenantID, userID, conversationID, correlationID, now, "queued")
		command := cancellationTestCommand(cancellationID, tenantID, userID, runID, correlationID, accepted.RunVersion, "a")

		result, err := store.RequestCancellation(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Settled || result.Replayed || result.RunStatus != statemachine.RunCancelled || result.RunVersion != accepted.RunVersion+1 || result.CancelGeneration != 1 || result.CancellationStatus != "settled" {
			t.Fatalf("unexpected cancellation result: %#v", result)
		}
		replay, err := store.RequestCancellation(ctx, command)
		if err != nil || !replay.Replayed || replay.SettlementEventID != result.SettlementEventID || replay.RunVersion != result.RunVersion {
			t.Fatalf("replay=%#v error=%v", replay, err)
		}
		conflict := command
		conflict.RequestHash = strings.Repeat("e", 64)
		if _, err = store.RequestCancellation(ctx, conflict); !errors.Is(err, ErrRunConflict) {
			t.Fatalf("different intent replay error=%v", err)
		}

		var runStatus, cancellationStatus, jobStatus string
		var cancellationVersion, requestEvents, settlementEvents int
		if err = admin.QueryRow(ctx, `SELECT r.status,c.status,c.version,j.status,
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run_cancellation' AND aggregate_id=$4 AND event_type='RunCancellationRequested'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunCancelled')
			FROM agent.runs r JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
			JOIN agent.jobs j ON j.tenant_id=r.tenant_id AND j.command_id=$3 WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, accepted.StartCommandID, cancellationID).Scan(&runStatus, &cancellationStatus, &cancellationVersion, &jobStatus, &requestEvents, &settlementEvents); err != nil {
			t.Fatal(err)
		}
		if runStatus != "cancelled" || cancellationStatus != "settled" || cancellationVersion != 2 || jobStatus != "cancelled" || requestEvents != 1 || settlementEvents != 1 {
			t.Fatalf("run=%s cancellation=%s/v%d job=%s request_events=%d settlement_events=%d", runStatus, cancellationStatus, cancellationVersion, jobStatus, requestEvents, settlementEvents)
		}
	})

	t.Run("executing run invalidates its exact execution right", func(t *testing.T) {
		const tenantID = "ce200000-0000-4000-8000-000000000001"
		const userID = "ce200000-0000-4000-8000-000000000002"
		const runID = "ce200000-0000-4000-8000-000000000003"
		const conversationID = "ce200000-0000-4000-8000-000000000004"
		const correlationID = "ce200000-0000-4000-8000-000000000005"
		const cancellationID = "ce200000-0000-4000-8000-000000000006"
		seedCancellationIdentity(t, ctx, admin, tenantID, userID, "executing-cancel@example.invalid", now)
		accepted := acceptCancellationRun(t, ctx, store, runID, tenantID, userID, conversationID, correlationID, now, "executing")
		claim := claimCancellationRun(t, ctx, store, accepted, tenantID, runID, correlationID, storeEpoch, "executing")
		result, err := store.RequestCancellation(ctx, cancellationTestCommand(cancellationID, tenantID, userID, runID, correlationID, claim.RunVersion, "b"))
		if err != nil || !result.Settled || result.RunStatus != statemachine.RunCancelled {
			t.Fatalf("result=%#v error=%v", result, err)
		}
		if _, err = store.CompleteRunTerminal(ctx, CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: statemachine.RunSucceeded, ResultHash: "late-result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://cancellation/late-run", Hash: "late-run"}, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://cancellation/late-attempt", Hash: "late-attempt"}}); !errors.Is(err, ErrExecutionRightConflict) {
			t.Fatalf("stale worker completion error=%v", err)
		}

		var runStatus, attemptStatus, inboxStatus, jobStatus string
		var fence, attemptVersion, attemptCompletedEvents int
		var executionRightCleared bool
		if err = admin.QueryRow(ctx, `SELECT r.status,r.current_fence,
			r.active_command_id IS NULL AND r.active_attempt_id IS NULL AND r.lease_token_hash IS NULL AND r.lease_expires_at IS NULL,
			a.status,a.version,i.status,j.status,
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='job_attempt' AND aggregate_id=$3 AND event_type='JobAttemptCompleted')
			FROM agent.runs r JOIN agent.job_attempts a ON a.tenant_id=r.tenant_id AND a.id=$3
			JOIN agent.inbox i ON i.tenant_id=a.tenant_id AND i.owner_attempt_id=a.id
			JOIN agent.jobs j ON j.tenant_id=a.tenant_id AND j.id=a.job_id
			WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, claim.AttemptID).Scan(&runStatus, &fence, &executionRightCleared, &attemptStatus, &attemptVersion, &inboxStatus, &jobStatus, &attemptCompletedEvents); err != nil {
			t.Fatal(err)
		}
		if runStatus != "cancelled" || fence != int(claim.Fence+1) || !executionRightCleared || attemptStatus != "abandoned" || attemptVersion != 2 || inboxStatus != "completed" || jobStatus != "cancelled" || attemptCompletedEvents != 1 {
			t.Fatalf("run=%s fence=%d cleared=%v attempt=%s/v%d inbox=%s job=%s attempt_events=%d", runStatus, fence, executionRightCleared, attemptStatus, attemptVersion, inboxStatus, jobStatus, attemptCompletedEvents)
		}
	})

	t.Run("tool blocker enters durable terminating state", func(t *testing.T) {
		const tenantID = "ce300000-0000-4000-8000-000000000001"
		const userID = "ce300000-0000-4000-8000-000000000002"
		const runID = "ce300000-0000-4000-8000-000000000003"
		const conversationID = "ce300000-0000-4000-8000-000000000004"
		const correlationID = "ce300000-0000-4000-8000-000000000005"
		const cancellationID = "ce300000-0000-4000-8000-000000000006"
		seedCancellationIdentity(t, ctx, admin, tenantID, userID, "blocked-cancel@example.invalid", now)
		accepted := acceptCancellationRun(t, ctx, store, runID, tenantID, userID, conversationID, correlationID, now, "blocked")
		claim := claimCancellationRun(t, ctx, store, accepted, tenantID, runID, correlationID, storeEpoch, "blocked")
		tools, err := store.RequestTools(ctx, RequestToolsCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, StepID: "cancel-blocker", JoinPolicy: "all", QuorumCount: 1, PlanResultHash: "cancel-blocker-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://cancellation/blocked/attempt", Hash: "blocked-attempt"}, ToolRequests: []ToolRequest{{ToolName: "github_create_issue", DescriptorSnapshotID: "github_create_issue@sha256:cancellation", NormalizedInputRef: "encrypted://cancellation/blocked/input", RequestHash: "blocked-tool-request", EffectClass: "idempotent_write", EffectKey: "issue:cancellation", EffectScope: "tenant:github:cancellation", ProviderID: "github", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 1, MaxAttempts: 4, RequestedEvent: PayloadPointer{Ref: "encrypted://cancellation/blocked/requested", Hash: "blocked-requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://cancellation/blocked/execute", Hash: "blocked-execute"}}}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := store.RequestCancellation(ctx, cancellationTestCommand(cancellationID, tenantID, userID, runID, correlationID, tools.RunVersion, "c"))
		if err != nil || result.Settled || result.CancellationStatus != "terminating" || result.ReconcileCommandID == "" || result.RunStatus != statemachine.RunWaitingTool {
			t.Fatalf("result=%#v error=%v", result, err)
		}

		var runStatus, cancellationStatus, toolStatus, reconcileJobStatus string
		var cancellationVersion, cancelGeneration, requestedEvents, cancelledEvents int
		if err = admin.QueryRow(ctx, `SELECT r.status,r.cancel_generation,c.status,c.version,t.status,j.status,
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run_cancellation' AND aggregate_id=$4 AND event_type='RunCancellationRequested'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunCancelled')
			FROM agent.runs r JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
			JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.id=$3
			JOIN agent.jobs j ON j.tenant_id=r.tenant_id AND j.command_id=$5
			WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, tools.ToolCalls[0].ToolCallID, cancellationID, result.ReconcileCommandID).Scan(&runStatus, &cancelGeneration, &cancellationStatus, &cancellationVersion, &toolStatus, &reconcileJobStatus, &requestedEvents, &cancelledEvents); err != nil {
			t.Fatal(err)
		}
		if runStatus != "waiting_tool" || cancelGeneration != 1 || cancellationStatus != "terminating" || cancellationVersion != 2 || toolStatus != "requested" || reconcileJobStatus != "pending" || requestedEvents != 1 || cancelledEvents != 0 {
			t.Fatalf("run=%s generation=%d cancellation=%s/v%d tool=%s reconcile_job=%s request_events=%d cancelled_events=%d", runStatus, cancelGeneration, cancellationStatus, cancellationVersion, toolStatus, reconcileJobStatus, requestedEvents, cancelledEvents)
		}
		toolID := tools.ToolCalls[0].ToolCallID
		reconcileCommand := ReconcileRunCancellationCommand{CancellationID: cancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedCancellationVersion: 2, Actor: json.RawMessage(`{"kind":"service","id":"run-cancellation-reconciler"}`), CorrelationID: correlationID, ToolCancelledEvents: map[string]PayloadPointer{toolID: {Ref: "encrypted://cancellation/blocked/tool-cancelled", Hash: strings.Repeat("5", 64)}}}
		reconciled, err := store.ReconcileCancellation(ctx, reconcileCommand)
		if err != nil || !reconciled.Settled || reconciled.CancelledToolCalls != 1 || reconciled.RemainingBlockers != 0 || reconciled.CancellationVersion != 3 || reconciled.RunVersion != tools.RunVersion+1 {
			t.Fatalf("reconciled=%#v error=%v", reconciled, err)
		}
		replay, err := store.ReconcileCancellation(ctx, reconcileCommand)
		if err != nil || !replay.Settled || replay.SettlementEventID != reconciled.SettlementEventID || replay.CancellationVersion != reconciled.CancellationVersion || replay.RunVersion != reconciled.RunVersion {
			t.Fatalf("reconcile replay=%#v error=%v", replay, err)
		}
		var effectStatus string
		if err = admin.QueryRow(ctx, `SELECT r.status,c.status,t.status,j.status,e.status,
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$3 AND event_type='ToolCallCancelled'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunCancelled')
			FROM agent.runs r JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
			JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.id=$3
			JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id
			JOIN agent.jobs j ON j.tenant_id=r.tenant_id AND j.command_id=$4
			WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, toolID, result.ReconcileCommandID).Scan(&runStatus, &cancellationStatus, &toolStatus, &reconcileJobStatus, &effectStatus, &requestedEvents, &cancelledEvents); err != nil {
			t.Fatal(err)
		}
		if runStatus != "cancelled" || cancellationStatus != "settled" || toolStatus != "cancelled" || reconcileJobStatus != "cancelled" || effectStatus != "failed" || requestedEvents != 1 || cancelledEvents != 1 {
			t.Fatalf("settled run=%s cancellation=%s tool=%s effect=%s reconcile_job=%s tool_events=%d run_events=%d", runStatus, cancellationStatus, toolStatus, effectStatus, reconcileJobStatus, requestedEvents, cancelledEvents)
		}
	})

	t.Run("executing effect remains an authoritative blocker", func(t *testing.T) {
		const tenantID = "ce400000-0000-4000-8000-000000000001"
		const userID = "ce400000-0000-4000-8000-000000000002"
		const runID = "ce400000-0000-4000-8000-000000000003"
		const conversationID = "ce400000-0000-4000-8000-000000000004"
		const correlationID = "ce400000-0000-4000-8000-000000000005"
		const cancellationID = "ce400000-0000-4000-8000-000000000006"
		seedCancellationIdentity(t, ctx, admin, tenantID, userID, "executing-effect-cancel@example.invalid", now)
		accepted := acceptCancellationRun(t, ctx, store, runID, tenantID, userID, conversationID, correlationID, now, "effect-blocked")
		runClaim := claimCancellationRun(t, ctx, store, accepted, tenantID, runID, correlationID, storeEpoch, "effect-blocked")
		requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "effect-blocker", JoinPolicy: "all", QuorumCount: 1, PlanResultHash: "effect-blocker-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://cancellation/effect/attempt", Hash: "effect-attempt"}, ToolRequests: []ToolRequest{{ToolName: "github_create_issue", DescriptorSnapshotID: "github_create_issue@sha256:effect-cancellation", NormalizedInputRef: "encrypted://cancellation/effect/input", RequestHash: "effect-tool-request", EffectClass: "idempotent_write", EffectKey: "issue:effect-cancellation", EffectScope: "tenant:github:effect-cancellation", ProviderID: "github", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 1, MaxAttempts: 4, RequestedEvent: PayloadPointer{Ref: "encrypted://cancellation/effect/requested", Hash: "effect-requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://cancellation/effect/execute", Hash: "effect-execute"}}}})
		if err != nil {
			t.Fatal(err)
		}
		tool := requested.ToolCalls[0]
		toolClaim, err := store.ClaimTool(ctx, ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: "encrypted://cancellation/effect/execute", PayloadHash: "effect-execute"}, ConsumerName: "tool-worker", WorkerID: "effect-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: PayloadPointer{Ref: "encrypted://cancellation/effect/started", Hash: "effect-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://cancellation/effect/attempt-started", Hash: "effect-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://cancellation/effect/attempt-expired", Hash: "effect-attempt-expired"}})
		if err != nil {
			t.Fatal(err)
		}
		requestedCancellation, err := store.RequestCancellation(ctx, cancellationTestCommand(cancellationID, tenantID, userID, runID, correlationID, requested.RunVersion, "6"))
		if err != nil || requestedCancellation.Settled {
			t.Fatalf("requested cancellation=%#v error=%v", requestedCancellation, err)
		}
		reconciled, err := store.ReconcileCancellation(ctx, ReconcileRunCancellationCommand{CancellationID: cancellationID, TenantID: tenantID, StoreEpoch: storeEpoch, ExpectedCancellationVersion: 2, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolCancelledEvents: map[string]PayloadPointer{}})
		if err != nil || reconciled.Settled || reconciled.CancelledToolCalls != 0 || reconciled.RemainingBlockers != 1 || reconciled.CancellationVersion != 3 || !reconciled.NextAttemptAt.After(now) {
			t.Fatalf("reconciled=%#v error=%v", reconciled, err)
		}
		var runStatus, cancellationStatus, toolStatus, effectStatus, attemptStatus string
		var cancellationVersion, cancelledToolEvents, cancelledRunEvents int
		if err = admin.QueryRow(ctx, `SELECT r.status,c.status,c.version,t.status,e.status,a.status,
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$3 AND event_type='ToolCallCancelled'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunCancelled')
			FROM agent.runs r JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
			JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.id=$3
			JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id
			JOIN agent.job_attempts a ON a.tenant_id=t.tenant_id AND a.id=$4
			WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, tool.ToolCallID, toolClaim.AttemptID).Scan(&runStatus, &cancellationStatus, &cancellationVersion, &toolStatus, &effectStatus, &attemptStatus, &cancelledToolEvents, &cancelledRunEvents); err != nil {
			t.Fatal(err)
		}
		if runStatus != "waiting_tool" || cancellationStatus != "terminating" || cancellationVersion != 3 || toolStatus != "executing" || effectStatus != "executing" || attemptStatus != "running" || cancelledToolEvents != 0 || cancelledRunEvents != 0 {
			t.Fatalf("run=%s cancellation=%s/v%d tool=%s effect=%s attempt=%s tool_events=%d run_events=%d", runStatus, cancellationStatus, cancellationVersion, toolStatus, effectStatus, attemptStatus, cancelledToolEvents, cancelledRunEvents)
		}
	})
}

func seedCancellationIdentity(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenantID, userID, email string, now time.Time) {
	t.Helper()
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,$2,'en','active')`, userID, email); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal',$2,'active','US',$3)`, tenantID, email, userID); err != nil {
		t.Fatal(err)
	}
	seedExecutionBehavior(t, ctx, admin, tenantID, userID, "route_planner", now)
}

func acceptCancellationRun(t *testing.T, ctx context.Context, store RunStore, runID, tenantID, userID, conversationID, correlationID string, now time.Time, prefix string) AcceptedRun {
	t.Helper()
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://cancellation/" + prefix + "/accepted", Hash: prefix + "-accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://cancellation/" + prefix + "/queued", Hash: prefix + "-queued"}, StartCommand: PayloadPointer{Ref: "encrypted://cancellation/" + prefix + "/start", Hash: prefix + "-start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func claimCancellationRun(t *testing.T, ctx context.Context, store RunStore, accepted AcceptedRun, tenantID, runID, correlationID, storeEpoch, prefix string) RunClaim {
	t.Helper()
	claim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: "encrypted://cancellation/" + prefix + "/start", PayloadHash: prefix + "-start"}, ConsumerName: "agent-run-worker", WorkerID: "worker-" + prefix, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://cancellation/" + prefix + "/started", Hash: prefix + "-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://cancellation/" + prefix + "/attempt-started", Hash: prefix + "-attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://cancellation/" + prefix + "/attempt-expired", Hash: prefix + "-attempt-expired"}})
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func cancellationTestCommand(cancellationID, tenantID, userID, runID, correlationID string, runVersion uint64, hashDigit string) RequestRunCancellationCommand {
	return RequestRunCancellationCommand{CancellationID: cancellationID, TenantID: tenantID, UserID: userID, RunID: runID, ExpectedRunVersion: runVersion, Reason: "user_requested", RequestHash: strings.Repeat(hashDigit, 64), CorrelationID: correlationID, Actor: json.RawMessage(`{"kind":"user"}`), RequestEvent: PayloadPointer{Ref: "encrypted://cancellation/requested/" + cancellationID, Hash: strings.Repeat("1", 64)}, SettlementEvent: PayloadPointer{Ref: "encrypted://cancellation/settled/" + cancellationID, Hash: strings.Repeat("2", 64)}, AttemptCancelledEvent: PayloadPointer{Ref: "encrypted://cancellation/attempt/" + cancellationID, Hash: strings.Repeat("3", 64)}, ReconcileCommand: PayloadPointer{Ref: "encrypted://cancellation/reconcile/" + cancellationID, Hash: strings.Repeat("4", 64)}}
}
