//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/scheduler"
	"github.com/langshift/lites/internal/security/opaque"
)

type schedulerFixture struct{ tenant, user, run, conversation, correlation string }

func TestSchedulerDispatchLeasesAreGlobalDurableAndRecoverable(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	agent := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agent.Close()
	schedulerPool := schedulerTestPool(t, ctx)
	defer schedulerPool.Close()
	const storeEpoch = "4e000000-0000-4000-8000-000000000001"
	now := time.Date(2026, time.July, 14, 20, 0, 0, 0, time.UTC)
	runStore := RunStore{Pool: agent, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x81}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "scheduled-run", Pepper: bytes.Repeat([]byte{0x82}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	fixtures := []schedulerFixture{
		{"4e000000-0000-4000-8000-000000000010", "4e000000-0000-4000-8000-000000000011", "4e000000-0000-4000-8000-000000000012", "4e000000-0000-4000-8000-000000000013", "4e000000-0000-4000-8000-000000000014"},
		{"4e000000-0000-4000-8000-000000000020", "4e000000-0000-4000-8000-000000000021", "4e000000-0000-4000-8000-000000000022", "4e000000-0000-4000-8000-000000000023", "4e000000-0000-4000-8000-000000000024"},
	}
	accepted := make(map[string]AcceptedRun, len(fixtures))
	commands := make(map[string]AcceptRunCommand, len(fixtures))
	for index, item := range fixtures {
		if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,$2,'en','active')`, item.user, "scheduler-owner-"+string(rune('a'+index))+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal',$2,'active','US',$3)`, item.tenant, "Scheduler Tenant "+string(rune('A'+index)), item.user); err != nil {
			t.Fatal(err)
		}
		seedExecutionBehavior(t, ctx, admin, item.tenant, item.user, "route_planner", now)
		command := AcceptRunCommand{RunID: item.run, TenantID: item.tenant, UserID: item.user, ConversationID: item.conversation, CorrelationID: item.correlation, DueAt: now.Add(time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: PayloadPointer{Ref: "encrypted://scheduler/accepted/" + item.run, Hash: "accepted"}, QueuedEvent: PayloadPointer{Ref: "encrypted://scheduler/queued/" + item.run, Hash: "queued"}, StartCommand: PayloadPointer{Ref: "encrypted://scheduler/start/" + item.run, Hash: "start"}, QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 1, MaxAttempts: 5}
		result, err := runStore.Accept(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		accepted[item.run], commands[item.run] = result, command
		if _, err = admin.Exec(ctx, `UPDATE agent.outbox SET status='published',published_at=$1 WHERE command_id=$2`, now, result.StartCommandID); err != nil {
			t.Fatal(err)
		}
	}
	config := scheduler.Config{Resources: map[string]scheduler.ResourcePolicy{"llm": {Capacity: 2, InteractiveReserved: 1, BackgroundReserved: 0, HighPriorityThreshold: 90, HighPriorityMaxPercentage: 50, QuantumUnits: 1}}, DefaultTenant: scheduler.TenantPolicy{Weight: 1, ActiveConcurrencyCap: 2, BurstUnits: 10, RefillUnitsPerSecond: 10}, Tenants: map[string]scheduler.TenantPolicy{}, BatchLimit: 2}
	store := SchedulerStore{Pool: schedulerPool, Epochs: executionEpochStub{epoch: storeEpoch}, ResourceTokens: opaque.Manager{Purpose: "scheduler-resource", Pepper: bytes.Repeat([]byte{0x83}, 32)}, DispatchTokens: opaque.Manager{Purpose: "scheduler-dispatch", Pepper: bytes.Repeat([]byte{0x84}, 32)}, ResourceLeaseTTL: time.Minute, DispatchLeaseTTL: 30 * time.Second, RedeliveryDelay: time.Minute, RetryDelay: 10 * time.Second, Now: func() time.Time { return now }}

	resourceClaim, err := store.claimResource(ctx, config, "llm", "scheduler-a", storeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.claimResource(ctx, config, "llm", "scheduler-b", storeEpoch); !errors.Is(err, ErrSchedulerBusy) {
		t.Fatalf("concurrent resource claim: %v", err)
	}
	candidates, err := store.listCandidates(ctx, resourceClaim, 10)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates=%d error=%v", len(candidates), err)
	}
	if err = store.abortResource(ctx, resourceClaim); err != nil {
		t.Fatal(err)
	}
	dispatches, err := store.PlanResource(ctx, config, scheduler.Active{}, "llm", "scheduler-a", storeEpoch, 10)
	if err != nil || len(dispatches) != 2 {
		t.Fatalf("dispatches=%d error=%v", len(dispatches), err)
	}
	var invisible int
	if err = schedulerPool.QueryRow(ctx, `SELECT count(*) FROM agent.jobs`).Scan(&invisible); err != nil || invisible != 0 {
		t.Fatalf("scheduler direct cross-tenant visibility=%d error=%v", invisible, err)
	}
	empty, err := store.PlanResource(ctx, config, scheduler.Active{}, "llm", "scheduler-b", storeEpoch, 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("leased jobs redispatched=%d error=%v", len(empty), err)
	}

	first, second := dispatches[0], dispatches[1]
	wrong := first
	wrong.LeaseToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err = store.MarkDispatched(ctx, wrong); !errors.Is(err, ErrSchedulerConflict) {
		t.Fatalf("wrong dispatch token: %v", err)
	}
	firstFixture := fixtureByTenant(fixtures, first.Candidate.TenantID)
	firstAccepted := accepted[firstFixture.run]
	firstCommand := commands[firstFixture.run]
	workerClaim, err := runStore.ClaimStart(ctx, ClaimRunCommand{Command: first.Candidate.Command.Delivered(), ConsumerName: "agent-run-worker", WorkerID: "worker-after-fast-delivery", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: firstFixture.correlation, RunEvent: PayloadPointer{Ref: "encrypted://scheduler/run-started", Hash: "run-started"}, AttemptStartedEvent: PayloadPointer{Ref: "encrypted://scheduler/attempt-started", Hash: "attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://scheduler/attempt-expired", Hash: "attempt-expired"}})
	if err != nil || workerClaim.JobID != firstAccepted.StartJobID || firstCommand.StartCommand.Hash != first.Candidate.Command.PayloadHash {
		t.Fatalf("worker claim=%#v error=%v", workerClaim, err)
	}
	if err = store.MarkDispatched(ctx, first); err != nil {
		t.Fatalf("broker ACK after worker claim: %v", err)
	}
	if err = store.DeferDispatch(ctx, second, "broker_unavailable"); err != nil {
		t.Fatal(err)
	}
	var firstStatus, secondStatus, secondError string
	var firstLeaseCleared, secondLeaseCleared bool
	var secondAvailable time.Time
	if err = admin.QueryRow(ctx, `SELECT a.status,a.dispatch_lease_hash IS NULL,b.status,b.dispatch_lease_hash IS NULL,b.available_at,b.last_error_code FROM agent.jobs a JOIN agent.jobs b ON b.id=$2 WHERE a.id=$1`, first.Candidate.ID, second.Candidate.ID).Scan(&firstStatus, &firstLeaseCleared, &secondStatus, &secondLeaseCleared, &secondAvailable, &secondError); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "running" || !firstLeaseCleared || secondStatus != "pending" || !secondLeaseCleared || !secondAvailable.Equal(now.Add(store.RetryDelay)) || secondError != "broker_unavailable" {
		t.Fatalf("first=%s/%v second=%s/%v available=%s error=%s", firstStatus, firstLeaseCleared, secondStatus, secondLeaseCleared, secondAvailable, secondError)
	}

	now = secondAvailable.Add(time.Microsecond)
	crashed, err := store.PlanResource(ctx, config, scheduler.Active{}, "llm", "scheduler-crashed", storeEpoch, 10)
	if err != nil || len(crashed) != 1 || crashed[0].Candidate.ID != second.Candidate.ID {
		t.Fatalf("crashed dispatch=%#v error=%v", crashed, err)
	}
	now = crashed[0].LeaseExpiresAt.Add(time.Microsecond)
	recovered, err := store.PlanResource(ctx, config, scheduler.Active{}, "llm", "scheduler-recovery", storeEpoch, 10)
	if err != nil || len(recovered) != 1 || recovered[0].Candidate.ID != second.Candidate.ID || recovered[0].Candidate.DispatchVersion != crashed[0].Candidate.DispatchVersion+1 {
		t.Fatalf("recovered dispatch=%#v error=%v", recovered, err)
	}
	if err = store.MarkDispatched(ctx, crashed[0]); !errors.Is(err, ErrSchedulerConflict) {
		t.Fatalf("stale crashed dispatcher: %v", err)
	}
	if err = store.DeferDispatch(ctx, recovered[0], "planned_test_stop"); err != nil {
		t.Fatal(err)
	}
	var resourceLeaseCleared bool
	var stateVersion int
	if err = admin.QueryRow(ctx, `SELECT lease_owner IS NULL AND lease_token_hash IS NULL AND lease_expires_at IS NULL,(scheduler_state->>'version')::int FROM agent.scheduler_resources WHERE resource_class='llm'`).Scan(&resourceLeaseCleared, &stateVersion); err != nil || !resourceLeaseCleared || stateVersion != 1 {
		t.Fatalf("resource lease cleared=%v state version=%d error=%v", resourceLeaseCleared, stateVersion, err)
	}
}

func fixtureByTenant(fixtures []schedulerFixture, tenant string) schedulerFixture {
	for _, item := range fixtures {
		if item.tenant == tenant {
			return item
		}
	}
	return schedulerFixture{}
}

func schedulerTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, os.Getenv("LITES_TEST_SCHEDULER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
