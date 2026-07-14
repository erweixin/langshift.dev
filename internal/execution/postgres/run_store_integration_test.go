//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

func TestAcceptRunIsAtomicReplaySafeAndTenantIsolated(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "2e000000-0000-4000-8000-000000000001"
	const otherTenantID = "2e000000-0000-4000-8000-000000000002"
	const userID = "2e000000-0000-4000-8000-000000000003"
	const runID = "2e000000-0000-4000-8000-000000000004"
	const conversationID = "2e000000-0000-4000-8000-000000000005"
	const correlationID = "2e000000-0000-4000-8000-000000000006"
	const storeEpoch = "2e000000-0000-4000-8000-000000000007"
	now := time.Date(2026, time.July, 14, 15, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'agent-owner@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Agent Owner','active','US',$3),($2,'enterprise','Other Tenant','active','US',NULL)`, tenantID, otherTenantID, userID); err != nil {
		t.Fatal(err)
	}
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x68}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }}
	command := AcceptRunCommand{
		RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID,
		DueAt: now.Add(time.Hour), ProfileSnapshotID: "route_planner@sha256:profile-v1",
		BudgetSnapshot: json.RawMessage(`{"max_steps":32,"max_cost_microunits":100000}`), Actor: json.RawMessage(`{"kind":"user","id":"2e000000-0000-4000-8000-000000000003"}`),
		AcceptedEvent: PayloadPointer{Ref: "encrypted://events/run-accepted", Hash: "accepted-hash"}, QueuedEvent: PayloadPointer{Ref: "encrypted://events/run-queued", Hash: "queued-hash"}, StartCommand: PayloadPointer{Ref: "encrypted://commands/run-start", Hash: "start-hash"},
		QueueClass: "interactive", Priority: 100,
	}
	const workers = 32
	var wait sync.WaitGroup
	var replayed atomic.Int64
	errorsFound := make(chan error, workers)
	results := make(chan AcceptedRun, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.Accept(ctx, command)
			if err != nil {
				errorsFound <- err
				return
			}
			if result.Replayed {
				replayed.Add(1)
			}
			results <- result
		}()
	}
	wait.Wait()
	close(errorsFound)
	close(results)
	for err := range errorsFound {
		t.Fatalf("concurrent accept: %v", err)
	}
	if replayed.Load() != workers-1 {
		t.Fatalf("replayed=%d, want %d", replayed.Load(), workers-1)
	}
	var first AcceptedRun
	for result := range results {
		if first.RunID == "" {
			first = result
		}
		if result.RunID != runID || result.RunVersion != 2 || result.Status != "queued" || result.StartCommandID != first.StartCommandID || result.StartJobID != first.StartJobID {
			t.Fatalf("non-convergent result: %#v first=%#v", result, first)
		}
	}
	var runs, events, outbox, jobs int
	var status string
	var version int
	var pendingCommand string
	if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.runs WHERE id=$1),(SELECT count(*) FROM agent.events WHERE aggregate_kind='run' AND aggregate_id=$1),(SELECT count(*) FROM agent.outbox WHERE aggregate_kind='run' AND aggregate_id=$1),(SELECT count(*) FROM agent.jobs WHERE command_id=$2),(SELECT status FROM agent.runs WHERE id=$1),(SELECT run_version FROM agent.runs WHERE id=$1),(SELECT pending_command_id::text FROM agent.runs WHERE id=$1)`, runID, first.StartCommandID).Scan(&runs, &events, &outbox, &jobs, &status, &version, &pendingCommand); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || events != 2 || outbox != 3 || jobs != 1 || status != "queued" || version != 2 || pendingCommand != first.StartCommandID {
		t.Fatalf("runs=%d events=%d outbox=%d jobs=%d status=%s version=%d pending=%s", runs, events, outbox, jobs, status, version, pendingCommand)
	}
	now = now.Add(5 * time.Minute)
	delayedReplay, err := store.Accept(ctx, command)
	if err != nil || !delayedReplay.Replayed || delayedReplay.StartCommandID != first.StartCommandID {
		t.Fatalf("delayed replay=%#v error=%v", delayedReplay, err)
	}

	conflict := command
	conflict.StartCommand.Hash = "different-intent"
	if _, err := store.Accept(ctx, conflict); !errors.Is(err, eventpostgres.ErrCommandConflict) {
		t.Fatalf("same run with different command intent: %v", err)
	}
	crossTenant := command
	crossTenant.TenantID = otherTenantID
	if _, err := store.Accept(ctx, crossTenant); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("cross-tenant run collision: %v", err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, otherTenantID); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.runs WHERE id=$1`, runID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("cross-tenant visible=%d error=%v", visible, err)
	}
}

func executionPool(t *testing.T, ctx context.Context, environment string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(environment)
	if value == "" {
		t.Fatalf("%s is required", environment)
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
