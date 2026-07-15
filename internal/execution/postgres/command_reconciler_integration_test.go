//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/scheduler"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestCommandReconcilerRestoresLostPendingAndExpiredRunningDelivery(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	agent := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agent.Close()
	schedulerPool := schedulerTestPool(t, ctx)
	defer schedulerPool.Close()

	const (
		storeEpoch   = "5e000000-0000-4000-8000-000000000001"
		tenantID     = "5e000000-0000-4000-8000-000000000002"
		userID       = "5e000000-0000-4000-8000-000000000003"
		runID        = "5e000000-0000-4000-8000-000000000004"
		conversation = "5e000000-0000-4000-8000-000000000005"
		correlation  = "5e000000-0000-4000-8000-000000000006"
	)
	now := time.Date(2026, time.July, 15, 2, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'command-reconciler@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Command Reconciler','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	seedExecutionBehavior(t, ctx, admin, tenantID, userID, "route_planner", now)
	runStore := RunStore{Pool: agent, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x91}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "redelivery-run", Pepper: bytes.Repeat([]byte{0x92}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	accepted, err := runStore.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversation, CorrelationID: correlation, DueAt: now.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://redelivery/accepted", Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://redelivery/queued", Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://redelivery/start", Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 1, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	var originalOutboxID string
	if err = admin.QueryRow(ctx, `UPDATE agent.outbox SET status='published',published_at=$1 WHERE command_id=$2 RETURNING id::text`, now, accepted.StartCommandID).Scan(&originalOutboxID); err != nil {
		t.Fatal(err)
	}

	reconciler := CommandReconcilerStore{Pool: agent, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, Epochs: executionEpochStub{epoch: storeEpoch}, IDKey: bytes.Repeat([]byte{0x93}, 32), ClaimSLO: time.Minute, BackoffBase: time.Second, BackoffLimit: 8 * time.Second, Now: func() time.Time { return now }}
	now = now.Add(reconciler.ClaimSLO + time.Microsecond)
	tenants, err := reconciler.ListReadyTenantIDs(ctx, storeEpoch, "", 10, 0, 1)
	if err != nil || len(tenants) != 1 || tenants[0] != tenantID {
		t.Fatalf("ready tenants=%v error=%v", tenants, err)
	}
	candidates, err := reconciler.ListCandidates(ctx, tenantID, storeEpoch, "", 10)
	if err != nil || len(candidates) != 1 || candidates[0].CommandID != accepted.StartCommandID || candidates[0].QueueGeneration != 1 {
		t.Fatalf("candidates=%#v error=%v", candidates, err)
	}
	request := RequestCommandRedelivery{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, ExpectedPayloadHash: "start", ExpectedQueueGeneration: 1, ReasonCode: RedeliveryReasonClaimSLOElapsed, Actor: json.RawMessage(`{"kind":"service","name":"command-reconciler"}`), CorrelationID: correlation, RequestedEvent: PayloadPointer{Ref: "encrypted://redelivery/requested/1", Hash: "redelivery-requested-1"}}
	var winners atomic.Int32
	results := make(chan CommandRedelivery, 32)
	errorsSeen := make(chan error, 32)
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, requestErr := reconciler.RequestRedelivery(ctx, request)
			if requestErr == nil {
				winners.Add(1)
				results <- result
				return
			}
			errorsSeen <- requestErr
		}()
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	if winners.Load() != 1 {
		t.Fatalf("redelivery winners=%d", winners.Load())
	}
	for requestErr := range errorsSeen {
		if !errors.Is(requestErr, ErrCommandNotRedeliverable) && !errors.Is(requestErr, ErrRedeliveryConflict) {
			t.Fatalf("unexpected concurrent request error=%v", requestErr)
		}
	}
	first := <-results
	if first.QueueGeneration != 2 || first.RedeliveryCount != 1 || first.CommandID != accepted.StartCommandID {
		t.Fatalf("first redelivery=%#v", first)
	}
	var outboxID, outboxCommandID, outboxStatus string
	var queueGeneration, redeliveryCount uint64
	var eventCount int
	if err = admin.QueryRow(ctx, `SELECT o.id::text,o.command_id::text,o.status,j.queue_generation,j.redelivery_count,(SELECT count(*) FROM agent.events e WHERE e.tenant_id=o.tenant_id AND e.event_type='CommandRedeliveryRequested') FROM agent.outbox o JOIN agent.jobs j ON j.tenant_id=o.tenant_id AND j.command_id=o.command_id WHERE o.command_id=$1`, accepted.StartCommandID).Scan(&outboxID, &outboxCommandID, &outboxStatus, &queueGeneration, &redeliveryCount, &eventCount); err != nil {
		t.Fatal(err)
	}
	if outboxID != originalOutboxID || outboxCommandID != accepted.StartCommandID || outboxStatus != "pending" || queueGeneration != 2 || redeliveryCount != 1 || eventCount != 1 {
		t.Fatalf("outbox=%s/%s/%s generation=%d count=%d events=%d", outboxID, outboxCommandID, outboxStatus, queueGeneration, redeliveryCount, eventCount)
	}

	now = first.AvailableAt
	if _, err = admin.Exec(ctx, `UPDATE agent.outbox SET status='published',published_at=$1 WHERE command_id=$2 AND status='pending'`, now, accepted.StartCommandID); err != nil {
		t.Fatal(err)
	}
	config := scheduler.Config{Resources: map[string]scheduler.ResourcePolicy{"llm": {Capacity: 1, InteractiveReserved: 1, HighPriorityThreshold: 90, HighPriorityMaxPercentage: 50, QuantumUnits: 1}}, DefaultTenant: scheduler.TenantPolicy{Weight: 1, ActiveConcurrencyCap: 2, BurstUnits: 10, RefillUnitsPerSecond: 10}, Tenants: map[string]scheduler.TenantPolicy{}, BatchLimit: 1}
	schedulerStore := SchedulerStore{Pool: schedulerPool, Epochs: executionEpochStub{epoch: storeEpoch}, ResourceTokens: opaque.Manager{Purpose: "redelivery-scheduler-resource", Pepper: bytes.Repeat([]byte{0x94}, 32)}, DispatchTokens: opaque.Manager{Purpose: "redelivery-scheduler-dispatch", Pepper: bytes.Repeat([]byte{0x95}, 32)}, ResourceLeaseTTL: time.Minute, DispatchLeaseTTL: 30 * time.Second, RedeliveryDelay: time.Minute, RetryDelay: 10 * time.Second, Now: func() time.Time { return now }}
	dispatches, err := schedulerStore.PlanResource(ctx, config, scheduler.Active{}, "llm", "redelivery-scheduler-1", storeEpoch, 10)
	if err != nil || len(dispatches) != 1 || dispatches[0].Candidate.Command.CommandID != accepted.StartCommandID {
		t.Fatalf("pending redelivery dispatches=%#v error=%v", dispatches, err)
	}
	firstClaim, err := runStore.ClaimStart(ctx, ClaimRunCommand{Command: dispatches[0].Candidate.Command.Delivered(), ConsumerName: "agent-run-worker", WorkerID: "redelivery-worker-1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlation, RunEvent: PayloadPointer{Ref: "encrypted://redelivery/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://redelivery/attempt-started/1", Hash: "attempt-started-1"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://redelivery/attempt-expired/1", Hash: "attempt-expired-1"}})
	if err != nil || firstClaim.Fence != 1 {
		t.Fatalf("first worker claim=%#v error=%v", firstClaim, err)
	}
	if err = schedulerStore.MarkDispatched(ctx, dispatches[0]); err != nil {
		t.Fatalf("broker ACK after initial claim: %v", err)
	}

	now = firstClaim.LeaseExpiresAt.Add(time.Microsecond)
	second, err := reconciler.RequestRedelivery(ctx, RequestCommandRedelivery{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, ExpectedPayloadHash: "start", ExpectedQueueGeneration: 2, ReasonCode: RedeliveryReasonClaimSLOElapsed, Actor: json.RawMessage(`{"kind":"service","name":"command-reconciler"}`), CorrelationID: correlation, RequestedEvent: PayloadPointer{Ref: "encrypted://redelivery/requested/2", Hash: "redelivery-requested-2"}})
	if err != nil || second.QueueGeneration != 3 || second.RedeliveryCount != 2 {
		t.Fatalf("running redelivery=%#v error=%v", second, err)
	}
	now = second.AvailableAt
	if _, err = admin.Exec(ctx, `UPDATE agent.outbox SET status='published',published_at=$1 WHERE command_id=$2 AND status='pending'`, now, accepted.StartCommandID); err != nil {
		t.Fatal(err)
	}
	dispatches, err = schedulerStore.PlanResource(ctx, config, scheduler.Active{}, "llm", "redelivery-scheduler-2", storeEpoch, 10)
	if err != nil || len(dispatches) != 1 || dispatches[0].Candidate.ID != accepted.StartJobID {
		t.Fatalf("running redelivery dispatches=%#v error=%v", dispatches, err)
	}
	secondClaim, err := runStore.ClaimStart(ctx, ClaimRunCommand{Command: dispatches[0].Candidate.Command.Delivered(), ConsumerName: "agent-run-worker", WorkerID: "redelivery-worker-2", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlation, RunEvent: PayloadPointer{Ref: "encrypted://redelivery/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://redelivery/attempt-started/2", Hash: "attempt-started-2"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://redelivery/attempt-expired/2", Hash: "attempt-expired-2"}})
	if err != nil || secondClaim.Fence != 2 || secondClaim.AttemptID == firstClaim.AttemptID || secondClaim.CommandID != firstClaim.CommandID {
		t.Fatalf("reclaimed worker=%#v error=%v", secondClaim, err)
	}
	if err = schedulerStore.MarkDispatched(ctx, dispatches[0]); err != nil {
		t.Fatalf("broker ACK after reclaim: %v", err)
	}
	_, err = reconciler.RequestRedelivery(ctx, RequestCommandRedelivery{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, ExpectedPayloadHash: "start", ExpectedQueueGeneration: 3, ReasonCode: RedeliveryReasonQueueGenerationLost, Actor: json.RawMessage(`{"kind":"service","name":"command-reconciler"}`), CorrelationID: correlation, RequestedEvent: PayloadPointer{Ref: "encrypted://redelivery/requested/3", Hash: "redelivery-requested-3"}})
	if !errors.Is(err, ErrCommandHasLiveOwner) {
		t.Fatalf("live owner redelivery error=%v", err)
	}
	var oldStatus string
	if err = admin.QueryRow(ctx, `SELECT status FROM agent.job_attempts WHERE id=$1`, firstClaim.AttemptID).Scan(&oldStatus); err != nil || oldStatus != "expired" {
		t.Fatalf("old attempt status=%s error=%v", oldStatus, err)
	}
}
