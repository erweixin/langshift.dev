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
	"github.com/langshift/lites/internal/security/opaque"
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
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	manager := opaque.Manager{Purpose: "agent-run-lease", Pepper: bytes.Repeat([]byte{0x71}, 32)}
	store.Epochs = executionEpochStub{epoch: storeEpoch}
	store.Tokens = manager
	store.LeaseTTL = 90 * time.Second
	claimCommand := ClaimRunCommand{
		Command: eventpostgres.DeliveredCommand{
			TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: first.StartCommandID,
			CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID,
			PayloadRef: command.StartCommand.Ref, PayloadHash: command.StartCommand.Hash,
		},
		ConsumerName: "agent-run-worker", WorkerID: "worker-us-1-a", CorrelationID: correlationID,
		Actor:               json.RawMessage(`{"kind":"service","id":"agent-run-worker"}`),
		RunEvent:            PayloadPointer{Ref: "encrypted://events/run-started", Hash: "run-started-hash"},
		AttemptStartedEvent: PayloadPointer{Ref: "encrypted://events/attempt-started", Hash: "attempt-started-hash"},
	}
	stale := claimCommand
	stale.Command.StoreEpoch = "2e000000-0000-4000-8000-000000000099"
	if _, err := store.ClaimStart(ctx, stale); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale epoch claim: %v", err)
	}

	var claimWait sync.WaitGroup
	claimResults := make(chan RunClaim, workers)
	claimErrors := make(chan error, workers)
	for range workers {
		claimWait.Add(1)
		go func() {
			defer claimWait.Done()
			result, claimErr := store.ClaimStart(ctx, claimCommand)
			if claimErr != nil {
				claimErrors <- claimErr
				return
			}
			claimResults <- result
		}()
	}
	claimWait.Wait()
	close(claimResults)
	close(claimErrors)
	var winner RunClaim
	var successfulClaims, busyClaims int
	for result := range claimResults {
		winner = result
		successfulClaims++
	}
	for claimErr := range claimErrors {
		if !errors.Is(claimErr, ErrClaimBusy) {
			t.Fatalf("unexpected claim error: %v", claimErr)
		}
		busyClaims++
	}
	if successfulClaims != 1 || busyClaims != workers-1 {
		t.Fatalf("successful claims=%d busy claims=%d", successfulClaims, busyClaims)
	}
	if winner.RunVersion != 3 || winner.Fence != 1 || winner.LeaseToken == "" || !winner.LeaseExpiresAt.Equal(now.Add(store.LeaseTTL)) {
		t.Fatalf("invalid winning claim: %#v", winner)
	}
	digest, err := manager.Digest(winner.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	var runStatus, activeCommand, activeAttempt, jobStatus, inboxStatus, attemptStatus string
	var runVersion, fence, runEvents, attemptEvents, totalOutbox, inboxRows, attemptRows int
	var runDigest, inboxDigest, attemptDigest []byte
	err = admin.QueryRow(ctx, `
		SELECT
		  r.status,r.run_version,r.active_command_id::text,r.active_attempt_id::text,r.current_fence,r.lease_token_hash,
		  j.status,i.status,i.lease_token_hash,a.status,a.lease_token_hash,
		  (SELECT count(*) FROM agent.events WHERE aggregate_kind='run' AND aggregate_id=$1),
		  (SELECT count(*) FROM agent.events WHERE aggregate_kind='job_attempt' AND aggregate_id=$3),
		  (SELECT count(*) FROM agent.outbox WHERE aggregate_id IN ($1,$3)),
		  (SELECT count(*) FROM agent.inbox WHERE tenant_id=$4 AND consumer_name=$5 AND command_id=$2),
		  (SELECT count(*) FROM agent.job_attempts WHERE tenant_id=$4 AND job_id=$6)
		FROM agent.runs r
		JOIN agent.jobs j ON j.id=$6
		JOIN agent.inbox i ON i.id=$7
		JOIN agent.job_attempts a ON a.id=$3
		WHERE r.id=$1`, runID, first.StartCommandID, winner.AttemptID, tenantID, claimCommand.ConsumerName, first.StartJobID, winner.InboxID).Scan(
		&runStatus, &runVersion, &activeCommand, &activeAttempt, &fence, &runDigest,
		&jobStatus, &inboxStatus, &inboxDigest, &attemptStatus, &attemptDigest,
		&runEvents, &attemptEvents, &totalOutbox, &inboxRows, &attemptRows,
	)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "executing" || runVersion != 3 || activeCommand != first.StartCommandID || activeAttempt != winner.AttemptID || fence != 1 || jobStatus != "running" || inboxStatus != "running" || attemptStatus != "running" {
		t.Fatalf("run=%s/v%d command=%s attempt=%s fence=%d job=%s inbox=%s job_attempt=%s", runStatus, runVersion, activeCommand, activeAttempt, fence, jobStatus, inboxStatus, attemptStatus)
	}
	if !bytes.Equal(runDigest, digest[:]) || !bytes.Equal(inboxDigest, digest[:]) || !bytes.Equal(attemptDigest, digest[:]) {
		t.Fatal("execution-right digest did not converge across run, inbox, and attempt")
	}
	if runEvents != 3 || attemptEvents != 1 || totalOutbox != 5 || inboxRows != 1 || attemptRows != 1 {
		t.Fatalf("run events=%d attempt events=%d outbox=%d inbox=%d attempts=%d", runEvents, attemptEvents, totalOutbox, inboxRows, attemptRows)
	}

	originalClaim := winner
	now = now.Add(30 * time.Second)
	winner, err = store.HeartbeatRun(ctx, winner)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !winner.LeaseExpiresAt.Equal(now.Add(store.LeaseTTL)) {
		t.Fatalf("heartbeat expiry=%s", winner.LeaseExpiresAt)
	}
	var runExpiry, inboxExpiry, attemptExpiry time.Time
	var attemptVersion int
	if err = admin.QueryRow(ctx, `SELECT r.lease_expires_at,i.lease_expires_at,a.lease_expires_at,a.version FROM agent.runs r JOIN agent.inbox i ON i.id=$2 JOIN agent.job_attempts a ON a.id=$3 WHERE r.id=$1`, runID, winner.InboxID, winner.AttemptID).Scan(&runExpiry, &inboxExpiry, &attemptExpiry, &attemptVersion); err != nil {
		t.Fatal(err)
	}
	if !runExpiry.Equal(winner.LeaseExpiresAt) || !inboxExpiry.Equal(winner.LeaseExpiresAt) || !attemptExpiry.Equal(winner.LeaseExpiresAt) || attemptVersion != 2 {
		t.Fatalf("heartbeat did not converge: run=%s inbox=%s attempt=%s version=%d", runExpiry, inboxExpiry, attemptExpiry, attemptVersion)
	}
	if _, err = store.HeartbeatRun(ctx, originalClaim); !errors.Is(err, ErrExecutionRightConflict) {
		t.Fatalf("stale heartbeat: %v", err)
	}
	wrongToken := winner
	wrongToken.LeaseToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err = store.HeartbeatRun(ctx, wrongToken); !errors.Is(err, ErrExecutionRightConflict) {
		t.Fatalf("wrong-token heartbeat: %v", err)
	}

	now = now.Add(30 * time.Second)
	complete := CompleteRunCommand{
		Claim: winner, ExpectedRunVersion: winner.RunVersion, TargetState: "succeeded", ResultHash: "result-sha256",
		Actor: json.RawMessage(`{"kind":"service","id":"agent-run-worker"}`), CorrelationID: correlationID,
		RunEvent: PayloadPointer{Ref: "encrypted://events/run-succeeded", Hash: "run-succeeded-hash"}, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://events/attempt-completed", Hash: "attempt-completed-hash"},
	}
	var completeWait sync.WaitGroup
	completeResults := make(chan CompletedRun, workers)
	completeErrors := make(chan error, workers)
	for range workers {
		completeWait.Add(1)
		go func() {
			defer completeWait.Done()
			result, completeErr := store.CompleteRunTerminal(ctx, complete)
			if completeErr != nil {
				completeErrors <- completeErr
				return
			}
			completeResults <- result
		}()
	}
	completeWait.Wait()
	close(completeResults)
	close(completeErrors)
	var completed CompletedRun
	var successfulCompletions, staleCompletions int
	for result := range completeResults {
		completed = result
		successfulCompletions++
	}
	for completeErr := range completeErrors {
		if !errors.Is(completeErr, ErrExecutionRightConflict) {
			t.Fatalf("unexpected completion error: %v", completeErr)
		}
		staleCompletions++
	}
	if successfulCompletions != 1 || staleCompletions != workers-1 || completed.RunVersion != 4 || completed.Status != "succeeded" || completed.AttemptStatus != "succeeded" {
		t.Fatalf("completion=%#v successful=%d stale=%d", completed, successfulCompletions, staleCompletions)
	}
	var activeCleared bool
	var jobVersion, finalAttemptVersion int
	err = admin.QueryRow(ctx, `
		SELECT r.status,r.run_version,
		       r.active_command_id IS NULL AND r.active_attempt_id IS NULL AND r.lease_token_hash IS NULL AND r.lease_expires_at IS NULL,
		       j.status,j.version,i.status,a.status,a.version,
		       (SELECT count(*) FROM agent.events WHERE aggregate_kind='run' AND aggregate_id=$1),
		       (SELECT count(*) FROM agent.events WHERE aggregate_kind='job_attempt' AND aggregate_id=$3),
		       (SELECT count(*) FROM agent.outbox WHERE aggregate_id IN ($1,$3))
		FROM agent.runs r
		JOIN agent.jobs j ON j.id=$2
		JOIN agent.inbox i ON i.id=$4
		JOIN agent.job_attempts a ON a.id=$3
		WHERE r.id=$1`, runID, winner.JobID, winner.AttemptID, winner.InboxID).Scan(
		&runStatus, &runVersion, &activeCleared, &jobStatus, &jobVersion, &inboxStatus, &attemptStatus, &finalAttemptVersion, &runEvents, &attemptEvents, &totalOutbox,
	)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "succeeded" || runVersion != 4 || !activeCleared || jobStatus != "succeeded" || jobVersion != 2 || inboxStatus != "completed" || attemptStatus != "succeeded" || finalAttemptVersion != 3 {
		t.Fatalf("terminal run=%s/v%d cleared=%v job=%s/v%d inbox=%s attempt=%s/v%d", runStatus, runVersion, activeCleared, jobStatus, jobVersion, inboxStatus, attemptStatus, finalAttemptVersion)
	}
	if runEvents != 4 || attemptEvents != 2 || totalOutbox != 7 {
		t.Fatalf("terminal run events=%d attempt events=%d outbox=%d", runEvents, attemptEvents, totalOutbox)
	}
}

type executionEpochStub struct {
	epoch string
	err   error
}

func (stub executionEpochStub) CurrentStoreEpoch(context.Context) (string, error) {
	return stub.epoch, stub.err
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
