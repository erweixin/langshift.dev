//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestRunCancellationRaceFaultInjection100(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "cf000000-0000-4000-8000-000000000001"
	const userID = "cf000000-0000-4000-8000-000000000002"
	const storeEpoch = "cf000000-0000-4000-8000-000000000003"
	now := time.Date(2026, time.July, 15, 11, 0, 0, 0, time.UTC)
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "cancel-race@example.invalid", now)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0xcf}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "run-cancellation-race", Pepper: bytes.Repeat([]byte{0xce}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}

	const repetitions = 100
	var cancelled, succeeded int
	for iteration := 1; iteration <= repetitions; iteration++ {
		runID := faultUUID(iteration*10 + 1)
		conversationID := faultUUID(iteration*10 + 2)
		correlationID := faultUUID(iteration*10 + 3)
		cancellationID := faultUUID(iteration*10 + 4)
		accepted := acceptCancellationRun(t, ctx, store, runID, tenantID, userID, conversationID, correlationID, now, fmt.Sprintf("race-%03d", iteration))
		claim := claimCancellationRun(t, ctx, store, accepted, tenantID, runID, correlationID, storeEpoch, fmt.Sprintf("race-%03d", iteration))

		start := make(chan struct{})
		var wait sync.WaitGroup
		var cancellationErr, completionErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_, cancellationErr = store.RequestCancellation(ctx, cancellationTestCommand(cancellationID, tenantID, userID, runID, correlationID, claim.RunVersion, "d"))
		}()
		go func() {
			defer wait.Done()
			<-start
			_, completionErr = store.CompleteRunTerminal(ctx, CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: statemachine.RunSucceeded, ResultHash: "race-success", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: PayloadPointer{Ref: "encrypted://cancellation/race/succeeded/" + runID, Hash: "race-succeeded"}, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://cancellation/race/attempt/" + runID, Hash: "race-attempt"}})
		}()
		close(start)
		wait.Wait()
		if cancellationErr != nil && !errors.Is(cancellationErr, ErrRunConflict) {
			t.Fatalf("iteration %d unexpected cancellation error: %v", iteration, cancellationErr)
		}
		if completionErr != nil && !errors.Is(completionErr, ErrExecutionRightConflict) {
			t.Fatalf("iteration %d completion error: %v", iteration, completionErr)
		}

		var status string
		var terminalEvents, cancellationRows int
		var executionRightCleared bool
		if err := admin.QueryRow(ctx, `SELECT status,
			active_command_id IS NULL AND active_attempt_id IS NULL AND lease_token_hash IS NULL AND lease_expires_at IS NULL,
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type IN ('RunSucceeded','RunCancelled')),
			(SELECT count(*) FROM agent.run_cancellations WHERE tenant_id=$1 AND run_id=$2)
			FROM agent.runs WHERE tenant_id=$1 AND id=$2`, tenantID, runID).Scan(&status, &executionRightCleared, &terminalEvents, &cancellationRows); err != nil {
			t.Fatalf("iteration %d inspect: %v", iteration, err)
		}
		if !executionRightCleared || terminalEvents != 1 {
			t.Fatalf("iteration %d status=%s cleared=%v terminal_events=%d cancellation_rows=%d", iteration, status, executionRightCleared, terminalEvents, cancellationRows)
		}
		switch status {
		case "cancelled":
			cancelled++
			if cancellationErr != nil || !errors.Is(completionErr, ErrExecutionRightConflict) || cancellationRows != 1 {
				t.Fatalf("iteration %d cancelled but cancellation=%v completion=%v rows=%d", iteration, cancellationErr, completionErr, cancellationRows)
			}
		case "succeeded":
			succeeded++
			if completionErr != nil || cancellationRows != 0 || cancellationErr != nil && !errors.Is(cancellationErr, ErrRunConflict) {
				t.Fatalf("iteration %d succeeded but cancellation=%v completion=%v rows=%d", iteration, cancellationErr, completionErr, cancellationRows)
			}
		default:
			t.Fatalf("iteration %d illegal terminal status %q", iteration, status)
		}
	}
	t.Logf("fault_injection={\"scenario\":\"run_cancellation_race\",\"repetitions\":%d,\"cancelled_wins\":%d,\"completion_wins\":%d,\"duplicate_terminal_events\":0,\"state_regressions\":0,\"lost_event_facts\":0}", repetitions, cancelled, succeeded)
}

func faultUUID(value int) string {
	return fmt.Sprintf("cf100000-0000-4000-8000-%012d", value)
}
