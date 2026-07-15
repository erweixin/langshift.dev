//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestOutcomeUnknownFaultInjection100(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "f9000000-0000-4000-8000-000000000001"
	const userID = "f9000000-0000-4000-8000-000000000002"
	const storeEpoch = "f9000000-0000-4000-8000-000000000003"
	start := time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "outcome-unknown-fault@example.invalid", start)

	const repetitions = 100
	var unknownRecorded, joinsBlocked, reconciliations, uniqueContinuations, staleResultsRejected int
	for iteration := 0; iteration < repetitions; iteration++ {
		clock := start.Add(time.Duration(iteration) * time.Hour)
		base := 40000 + iteration*10
		runID := outcomeUnknownFaultUUID(base + 1)
		conversationID := outcomeUnknownFaultUUID(base + 2)
		correlationID := outcomeUnknownFaultUUID(base + 3)
		prefix := fmt.Sprintf("unknown-%03d", iteration)
		pointer := func(name string) PayloadPointer {
			return PayloadPointer{Ref: "encrypted://outcome-unknown-fault/" + runID + "/" + name, Hash: prefix + "-" + name}
		}
		store := RunStore{
			Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }},
			IDKey: bytes.Repeat([]byte{0xf9}, 32), StoreEpoch: storeEpoch,
			Now: func() time.Time { return clock }, Epochs: executionEpochStub{epoch: storeEpoch},
			Tokens:   opaque.Manager{Purpose: "outcome-unknown-fault", Pepper: bytes.Repeat([]byte{0xf8}, 32)},
			LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver,
		}
		startPointer := pointer("start")
		accepted, err := store.Accept(ctx, AcceptRunCommand{
			RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID,
			DueAt: clock.Add(2 * time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production",
			BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`),
			AcceptedEvent: pointer("accepted"), QueuedEvent: pointer("queued"), StartCommand: startPointer,
			QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 2, MaxAttempts: 5,
		})
		if err != nil {
			t.Fatalf("iteration %d accept: %v", iteration, err)
		}
		runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{
			Command:      eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: startPointer.Ref, PayloadHash: startPointer.Hash},
			ConsumerName: "agent-worker", WorkerID: prefix + "-agent", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			RunEvent: pointer("run-started"), AttemptStartedEvent: pointer("run-attempt-started"), AttemptExpiredEvent: pointer("run-attempt-expired"),
		})
		if err != nil {
			t.Fatalf("iteration %d claim run: %v", iteration, err)
		}
		requested, err := store.RequestTools(ctx, RequestToolsCommand{
			Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: prefix, JoinPolicy: "all", QuorumCount: 1,
			PlanResultHash: prefix + "-plan", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			AttemptCompletedEvent: pointer("run-attempt-completed"),
			ToolRequests: []ToolRequest{{
				ToolName: "provider_write", DescriptorSnapshotID: "provider_write@sha256:contract",
				NormalizedInputRef: pointer("input").Ref, RequestHash: prefix + "-request",
				EffectClass: "reconcilable_write", EffectKey: "provider-write:" + runID,
				EffectScope: "tenant:outcome-unknown:" + runID, ProviderID: "fault-provider", Required: true,
				QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 1, MaxAttempts: 5,
				RequestedEvent: pointer("tool-requested"), ExecuteCommand: pointer("execute-tool"),
			}},
		})
		if err != nil {
			t.Fatalf("iteration %d request tool: %v", iteration, err)
		}
		tool := requested.ToolCalls[0]
		toolClaim, err := store.ClaimTool(ctx, ClaimToolCommand{
			Command:      eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: pointer("execute-tool").Ref, PayloadHash: pointer("execute-tool").Hash},
			ConsumerName: "tool-worker", WorkerID: prefix + "-tool", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			ToolStartedEvent: pointer("tool-started"), AttemptStartedEvent: pointer("tool-attempt-started"), AttemptExpiredEvent: pointer("tool-attempt-expired"),
		})
		if err != nil {
			t.Fatalf("iteration %d claim tool: %v", iteration, err)
		}
		reconcileAt := clock.Add(5 * time.Minute)
		unknownCommand := CompleteToolCommand{
			Claim: toolClaim, ExpectedToolVersion: toolClaim.ToolCallVersion, TargetState: statemachine.ToolCallOutcomeUnknown,
			ResultHash: prefix + "-unknown", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			ToolCompletedEvent: pointer("tool-outcome-unknown"), AttemptCompletedEvent: pointer("tool-attempt-completed"),
			GroupJoinedEvent: pointer("premature-group-joined"), RunResumeQueuedEvent: pointer("premature-resume"), ResumeCommand: pointer("premature-resume-command"),
			ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5,
		}
		unknown, err := store.CompleteEffectTool(ctx, unknownCommand, EffectCompletion{
			ReconciliationDueAt: reconcileAt, ReconcileCommand: pointer("reconcile"), ReconcileQueueClass: "background",
			ReconcileResource: "tool-reconciliation", ReconcilePriority: 40, ReconcileCostUnits: 1, ReconcileAttempts: 8,
		})
		if err != nil || unknown.Status != statemachine.ToolCallOutcomeUnknown || unknown.Resumed || unknown.ReconcileCommandID == "" {
			t.Fatalf("iteration %d unknown=%#v error=%v", iteration, unknown, err)
		}
		unknownRecorded++
		if _, staleErr := store.CompleteEffectTool(ctx, unknownCommand, EffectCompletion{
			ReconciliationDueAt: reconcileAt, ReconcileCommand: pointer("reconcile"), ReconcileQueueClass: "background",
			ReconcileResource: "tool-reconciliation", ReconcilePriority: 40, ReconcileCostUnits: 1, ReconcileAttempts: 8,
		}); !errors.Is(staleErr, ErrExecutionRightConflict) {
			t.Fatalf("iteration %d duplicate unknown completion error=%v", iteration, staleErr)
		}
		staleResultsRejected++

		var beforeStatus string
		var beforeJoined bool
		var beforeContinuations, reconcileJobs int
		if err = admin.QueryRow(ctx, `SELECT e.status,g.joined,
			(SELECT count(*) FROM agent.continuations WHERE tenant_id=$1 AND group_id=$3),
			(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND command_id=$4)
			FROM agent.tool_effects e JOIN agent.parallel_groups g ON g.tenant_id=e.tenant_id AND g.id=$3
			WHERE e.tenant_id=$1 AND e.tool_call_id=$2`, tenantID, tool.ToolCallID, requested.GroupID, unknown.ReconcileCommandID).Scan(&beforeStatus, &beforeJoined, &beforeContinuations, &reconcileJobs); err != nil {
			t.Fatalf("iteration %d inspect unknown: %v", iteration, err)
		}
		if beforeStatus != "outcome_unknown" || beforeJoined || beforeContinuations != 0 || reconcileJobs != 1 {
			t.Fatalf("iteration %d effect=%s joined=%v continuations=%d jobs=%d", iteration, beforeStatus, beforeJoined, beforeContinuations, reconcileJobs)
		}
		joinsBlocked++

		clock = reconcileAt
		reconcileCommand := ClaimReconciliationCommand{
			Command:      eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: unknown.ReconcileCommandID, CommandType: "ReconcileToolEffect", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: pointer("reconcile").Ref, PayloadHash: pointer("reconcile").Hash},
			ConsumerName: "reconciliation-worker", WorkerID: prefix + "-reconciler", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			AttemptStartedEvent: pointer("reconcile-attempt-started"), AttemptExpiredEvent: pointer("reconcile-attempt-expired"),
		}
		reconcileClaim, err := store.ClaimReconciliation(ctx, reconcileCommand)
		if err != nil || reconcileClaim.ProviderRequestID != toolClaim.ProviderRequestID || reconcileClaim.EffectID != toolClaim.EffectID {
			t.Fatalf("iteration %d reconciliation claim=%#v error=%v", iteration, reconcileClaim, err)
		}
		if _, duplicateErr := store.ClaimReconciliation(ctx, reconcileCommand); !errors.Is(duplicateErr, ErrClaimBusy) {
			t.Fatalf("iteration %d duplicate reconciliation claim error=%v", iteration, duplicateErr)
		}
		resourceRef := "provider://confirmed/" + runID
		completion := CompleteReconciliationCommand{
			Claim: reconcileClaim, ExpectedToolVersion: reconcileClaim.ToolCallVersion, ExpectedEffectVersion: reconcileClaim.EffectVersion,
			TargetState: statemachine.ToolCallSucceeded, ResultHash: prefix + "-confirmed", ExternalResourceRef: resourceRef,
			Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			ToolCompletedEvent: pointer("tool-reconciled"), AttemptCompletedEvent: pointer("reconcile-attempt-completed"),
			GroupJoinedEvent: pointer("group-joined"), RunResumeQueuedEvent: pointer("run-resume-queued"), ResumeCommand: pointer("resume-command"),
			ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5,
		}
		resolved, err := store.CompleteReconciliation(ctx, completion)
		if err != nil || !resolved.Resumed || resolved.Status != statemachine.ToolCallSucceeded || resolved.ContinuationID == "" || resolved.ResumeCommandID == "" {
			t.Fatalf("iteration %d resolved=%#v error=%v", iteration, resolved, err)
		}
		reconciliations++
		if _, duplicateErr := store.CompleteReconciliation(ctx, completion); !errors.Is(duplicateErr, ErrExecutionRightConflict) {
			t.Fatalf("iteration %d duplicate reconciliation completion error=%v", iteration, duplicateErr)
		}
		staleResultsRejected++
		completedReplay, duplicateErr := store.ClaimReconciliation(ctx, reconcileCommand)
		if !errors.Is(duplicateErr, ErrClaimCompleted) || !completedReplay.Completed {
			t.Fatalf("iteration %d completed reconciliation replay=%#v error=%v", iteration, completedReplay, duplicateErr)
		}

		var runStatus, toolStatus, effectStatus, providerRequestID, externalResource string
		var joined bool
		var continuations, unknownEvents, successEvents, effectRows int
		var reconciliationDue *time.Time
		err = admin.QueryRow(ctx, `SELECT r.status,t.status,e.status,e.provider_request_id,e.external_resource_ref,e.reconciliation_due_at,g.joined,
			(SELECT count(*) FROM agent.continuations WHERE tenant_id=$1 AND group_id=$4),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$3 AND event_type='ToolCallOutcomeUnknown'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$3 AND event_type='ToolCallSucceeded'),
			(SELECT count(*) FROM agent.tool_effects WHERE tenant_id=$1 AND tool_call_id=$3)
			FROM agent.runs r JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.run_id=r.id
			JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id
			JOIN agent.parallel_groups g ON g.tenant_id=t.tenant_id AND g.id=$4
			WHERE r.tenant_id=$1 AND r.id=$2 AND t.id=$3`, tenantID, runID, tool.ToolCallID, requested.GroupID).Scan(
			&runStatus, &toolStatus, &effectStatus, &providerRequestID, &externalResource, &reconciliationDue, &joined,
			&continuations, &unknownEvents, &successEvents, &effectRows,
		)
		if err != nil {
			t.Fatalf("iteration %d inspect resolved: %v", iteration, err)
		}
		if runStatus != "queued" || toolStatus != "succeeded" || effectStatus != "confirmed" || providerRequestID != toolClaim.ProviderRequestID || externalResource != resourceRef || reconciliationDue != nil || !joined || continuations != 1 || unknownEvents != 1 || successEvents != 1 || effectRows != 1 {
			t.Fatalf("iteration %d run=%s tool=%s effect=%s provider=%s/%s resource=%s due=%v joined=%v continuations=%d events=%d/%d rows=%d", iteration, runStatus, toolStatus, effectStatus, providerRequestID, toolClaim.ProviderRequestID, externalResource, reconciliationDue, joined, continuations, unknownEvents, successEvents, effectRows)
		}
		uniqueContinuations++
	}
	t.Logf("fault_injection={\"scenario\":\"outcome_unknown_reconciliation\",\"repetitions\":%d,\"unknown_results_recorded\":%d,\"premature_joins_blocked\":%d,\"reconciliations_confirmed\":%d,\"unique_continuations\":%d,\"stale_results_rejected\":%d,\"duplicate_external_effects\":0,\"duplicate_continuations\":0,\"lost_event_facts\":0}", repetitions, unknownRecorded, joinsBlocked, reconciliations, uniqueContinuations, staleResultsRejected)
}

func outcomeUnknownFaultUUID(value int) string {
	return fmt.Sprintf("f9100000-0000-4000-8000-%012d", value)
}
