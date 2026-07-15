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
		tools, err := store.RequestTools(ctx, RequestToolsCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, StepID: "cancel-blocker", JoinPolicy: "all", QuorumCount: 1, PlanResultHash: "cancel-blocker-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://cancellation/blocked/attempt", Hash: "blocked-attempt"}, ToolRequests: []ToolRequest{{ToolName: "web_search", DescriptorSnapshotID: "web_search@sha256:cancellation", NormalizedInputRef: "encrypted://cancellation/blocked/input", RequestHash: "blocked-tool-request", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 1, MaxAttempts: 4, RequestedEvent: PayloadPointer{Ref: "encrypted://cancellation/blocked/requested", Hash: "blocked-requested"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://cancellation/blocked/execute", Hash: "blocked-execute"}}}})
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
