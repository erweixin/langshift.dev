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

func TestExecutionDeliveryFaultInjection100(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "fd000000-0000-4000-8000-000000000001"
	const userID = "fd000000-0000-4000-8000-000000000002"
	const storeEpoch = "fd000000-0000-4000-8000-000000000003"
	const staleEpoch = "fd000000-0000-4000-8000-000000000004"
	start := time.Date(2026, time.July, 15, 23, 0, 0, 0, time.UTC)
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "execution-delivery-fault@example.invalid", start)

	const repetitions = 100
	var queueLossRecoveries, liveDuplicatesRejected, completedDuplicatesDeduplicated int
	var expiredLeasesRecovered, staleOwnersRejected, staleEpochCommandsRejected int
	for iteration := 0; iteration < repetitions; iteration++ {
		clock := start.Add(time.Duration(iteration) * time.Hour)
		base := 30000 + iteration*10
		runID := executionDeliveryFaultUUID(base + 1)
		conversationID := executionDeliveryFaultUUID(base + 2)
		correlationID := executionDeliveryFaultUUID(base + 3)
		prefix := fmt.Sprintf("delivery-%03d", iteration)
		pointer := func(name string) PayloadPointer {
			return PayloadPointer{Ref: "encrypted://execution-delivery-fault/" + runID + "/" + name, Hash: prefix + "-" + name}
		}
		store := RunStore{
			Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }},
			IDKey: bytes.Repeat([]byte{0xfd}, 32), StoreEpoch: storeEpoch,
			Now: func() time.Time { return clock }, Epochs: executionEpochStub{epoch: storeEpoch},
			Tokens:   opaque.Manager{Purpose: "execution-delivery-fault", Pepper: bytes.Repeat([]byte{0xfc}, 32)},
			LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver,
		}
		startPointer := pointer("start")
		accepted, err := store.Accept(ctx, AcceptRunCommand{
			RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID,
			DueAt: clock.Add(2 * time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production",
			BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`),
			AcceptedEvent: pointer("accepted"), QueuedEvent: pointer("queued"), StartCommand: startPointer,
			QueueClass: "interactive", ResourceClass: "llm", Priority: 60, CostUnits: 1, MaxAttempts: 5,
		})
		if err != nil {
			t.Fatalf("iteration %d accept: %v", iteration, err)
		}

		// The broker ACK was persisted, but no Worker observed the command. The
		// reconciler must requeue the same immutable command, not create a new one.
		if _, err = admin.Exec(ctx, `UPDATE agent.outbox SET status='published',published_at=$1 WHERE tenant_id=$2 AND command_id=$3`, clock, tenantID, accepted.StartCommandID); err != nil {
			t.Fatalf("iteration %d publish lost command: %v", iteration, err)
		}
		reconciler := CommandReconcilerStore{
			Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }},
			Epochs: executionEpochStub{epoch: storeEpoch}, IDKey: bytes.Repeat([]byte{0xfb}, 32),
			ClaimSLO: time.Minute, BackoffBase: time.Millisecond, BackoffLimit: time.Second,
			Now: func() time.Time { return clock },
		}
		clock = clock.Add(reconciler.ClaimSLO + time.Microsecond)
		redelivery, err := reconciler.RequestRedelivery(ctx, RequestCommandRedelivery{
			TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID,
			ExpectedPayloadHash: startPointer.Hash, ExpectedQueueGeneration: 1,
			ReasonCode: RedeliveryReasonClaimSLOElapsed, Actor: json.RawMessage(`{"kind":"service","name":"command-reconciler"}`),
			CorrelationID: correlationID, RequestedEvent: pointer("redelivery-requested"),
		})
		if err != nil || redelivery.CommandID != accepted.StartCommandID || redelivery.QueueGeneration != 2 || redelivery.RedeliveryCount != 1 {
			t.Fatalf("iteration %d redelivery=%#v error=%v", iteration, redelivery, err)
		}
		queueLossRecoveries++
		clock = redelivery.AvailableAt
		if _, err = admin.Exec(ctx, `UPDATE agent.outbox SET status='published',published_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, clock, tenantID, accepted.StartCommandID); err != nil {
			t.Fatalf("iteration %d republish: %v", iteration, err)
		}

		claimCommand := ClaimRunCommand{
			Command:      eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: startPointer.Ref, PayloadHash: startPointer.Hash},
			ConsumerName: "agent-run-worker", WorkerID: prefix + "-before-crash", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			RunEvent: pointer("run-started"), AttemptStartedEvent: pointer("attempt-started"), AttemptExpiredEvent: pointer("attempt-expired"),
		}
		oldEpochCommand := claimCommand
		oldEpochCommand.Command.StoreEpoch = staleEpoch
		if _, staleErr := store.ClaimStart(ctx, oldEpochCommand); !errors.Is(staleErr, ErrStaleEpoch) {
			t.Fatalf("iteration %d stale epoch error=%v", iteration, staleErr)
		}
		staleEpochCommandsRejected++
		oldClaim, err := store.ClaimStart(ctx, claimCommand)
		if err != nil {
			t.Fatalf("iteration %d initial claim: %v", iteration, err)
		}
		if _, duplicateErr := store.ClaimStart(ctx, claimCommand); !errors.Is(duplicateErr, ErrClaimBusy) {
			t.Fatalf("iteration %d live duplicate error=%v", iteration, duplicateErr)
		}
		liveDuplicatesRejected++

		// Simulate a Worker crash: it emits neither a heartbeat nor a result.
		// The next delivery may take ownership only after the durable lease expires.
		clock = oldClaim.LeaseExpiresAt.Add(time.Microsecond)
		claimCommand.WorkerID = prefix + "-after-crash"
		recovered, err := store.ClaimStart(ctx, claimCommand)
		if err != nil || recovered.Fence != oldClaim.Fence+1 || recovered.AttemptID == oldClaim.AttemptID || recovered.RunVersion != oldClaim.RunVersion {
			t.Fatalf("iteration %d recovered=%#v old=%#v error=%v", iteration, recovered, oldClaim, err)
		}
		expiredLeasesRecovered++
		if _, heartbeatErr := store.HeartbeatRun(ctx, oldClaim); !errors.Is(heartbeatErr, ErrExecutionRightConflict) {
			t.Fatalf("iteration %d stale heartbeat error=%v", iteration, heartbeatErr)
		}
		if _, completionErr := store.CompleteRunTerminal(ctx, CompleteRunCommand{
			Claim: oldClaim, ExpectedRunVersion: oldClaim.RunVersion, TargetState: statemachine.RunSucceeded,
			ResultHash: "stale-result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			RunEvent: pointer("stale-run-completed"), AttemptCompletedEvent: pointer("stale-attempt-completed"),
		}); !errors.Is(completionErr, ErrExecutionRightConflict) {
			t.Fatalf("iteration %d stale completion error=%v", iteration, completionErr)
		}
		staleOwnersRejected++
		completed, err := store.CompleteRunTerminal(ctx, CompleteRunCommand{
			Claim: recovered, ExpectedRunVersion: recovered.RunVersion, TargetState: statemachine.RunSucceeded,
			ResultHash: "durable-result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID,
			RunEvent: pointer("run-succeeded"), AttemptCompletedEvent: pointer("attempt-completed"),
		})
		if err != nil || completed.Status != statemachine.RunSucceeded {
			t.Fatalf("iteration %d completion=%#v error=%v", iteration, completed, err)
		}
		completedReplay, duplicateErr := store.ClaimStart(ctx, claimCommand)
		if !errors.Is(duplicateErr, ErrClaimCompleted) || !completedReplay.Completed || completedReplay.Fence != recovered.Fence {
			t.Fatalf("iteration %d completed duplicate=%#v error=%v", iteration, completedReplay, duplicateErr)
		}
		completedDuplicatesDeduplicated++

		var status, jobStatus, inboxStatus, oldAttemptStatus, newAttemptStatus string
		var fence, terminalEvents, startedAttempts, completedAttempts, redeliveryEvents int
		var executionRightCleared bool
		err = admin.QueryRow(ctx, `SELECT
			r.status,r.current_fence,
			r.active_command_id IS NULL AND r.active_attempt_id IS NULL AND r.lease_token_hash IS NULL AND r.lease_expires_at IS NULL,
			j.status,i.status,old.status,new.status,
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunSucceeded'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='job_attempt' AND aggregate_id IN ($4,$5) AND event_type='JobAttemptStarted'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='job_attempt' AND aggregate_id IN ($4,$5) AND event_type='JobAttemptCompleted'),
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='job' AND aggregate_id=$3 AND event_type='CommandRedeliveryRequested')
			FROM agent.runs r
			JOIN agent.jobs j ON j.tenant_id=r.tenant_id AND j.id=$3
			JOIN agent.inbox i ON i.tenant_id=r.tenant_id AND i.command_id=$6 AND i.consumer_name=$7
			JOIN agent.job_attempts old ON old.tenant_id=r.tenant_id AND old.id=$4
			JOIN agent.job_attempts new ON new.tenant_id=r.tenant_id AND new.id=$5
			WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, accepted.StartJobID, oldClaim.AttemptID, recovered.AttemptID, accepted.StartCommandID, claimCommand.ConsumerName).Scan(
			&status, &fence, &executionRightCleared, &jobStatus, &inboxStatus, &oldAttemptStatus, &newAttemptStatus,
			&terminalEvents, &startedAttempts, &completedAttempts, &redeliveryEvents,
		)
		if err != nil {
			t.Fatalf("iteration %d inspect: %v", iteration, err)
		}
		if status != "succeeded" || fence != 2 || !executionRightCleared || jobStatus != "succeeded" || inboxStatus != "completed" || oldAttemptStatus != "expired" || newAttemptStatus != "succeeded" || terminalEvents != 1 || startedAttempts != 2 || completedAttempts != 2 || redeliveryEvents != 1 {
			t.Fatalf("iteration %d run=%s/f%d/cleared=%v job=%s inbox=%s attempts=%s/%s events=%d/%d/%d/%d", iteration, status, fence, executionRightCleared, jobStatus, inboxStatus, oldAttemptStatus, newAttemptStatus, terminalEvents, startedAttempts, completedAttempts, redeliveryEvents)
		}
	}
	t.Logf("fault_injection={\"scenario\":\"execution_delivery_recovery\",\"repetitions\":%d,\"queue_loss_recoveries\":%d,\"live_duplicates_rejected\":%d,\"completed_duplicates_deduplicated\":%d,\"expired_leases_recovered\":%d,\"stale_owners_rejected\":%d,\"old_epoch_commands_rejected\":%d,\"duplicate_terminal_events\":0,\"state_regressions\":0,\"lost_event_facts\":0}", repetitions, queueLossRecoveries, liveDuplicatesRejected, completedDuplicatesDeduplicated, expiredLeasesRecovered, staleOwnersRejected, staleEpochCommandsRejected)
}

func executionDeliveryFaultUUID(value int) string {
	return fmt.Sprintf("fd100000-0000-4000-8000-%012d", value)
}
