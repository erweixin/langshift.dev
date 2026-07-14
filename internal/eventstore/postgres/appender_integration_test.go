//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAppendCommitsEventCursorAndOutboxExactlyOnce(t *testing.T) {
	ctx := context.Background()
	admin := poolFromEnv(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	service := poolFromEnv(t, ctx, "LITES_TEST_IDENTITY_DATABASE_URL")
	defer service.Close()

	const userID = "10000000-0000-0000-0000-000000000501"
	const tenantID = "20000000-0000-0000-0000-000000000501"
	const aggregateID = "30000000-0000-0000-0000-000000000501"
	const storeEpoch = "40000000-0000-0000-0000-000000000501"
	const correlationID = "50000000-0000-0000-0000-000000000501"
	now := time.Unix(1_800_000_500, 0).UTC()
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,'eventstore@lites.invalid',$2,'en','active')`, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','EventStore','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	appender := Appender{Now: func() time.Time { return now }}
	base := Input{Event: Event{ID: "60000000-0000-0000-0000-000000000501", TenantID: tenantID, UserID: userID, EventType: "EmailVerificationRequested", SchemaVersion: 1, AggregateKind: "user", AggregateID: aggregateID, AggregateVersion: 1, StoreEpoch: storeEpoch, OccurredAt: now, Actor: json.RawMessage(`{"kind":"system","id":"identity-service"}`), CorrelationID: correlationID, PayloadRef: "encrypted://events/501", PayloadHash: "event-hash-501"}, Commands: []OutboxCommand{{ID: "70000000-0000-0000-0000-000000000501", CommandID: "80000000-0000-0000-0000-000000000501", CommandType: "events.publish", PayloadRef: "encrypted://events/501", PayloadHash: "event-hash-501"}, {ID: "71000000-0000-0000-0000-000000000501", CommandID: "81000000-0000-0000-0000-000000000501", CommandType: "identity.email.verify", PayloadRef: "encrypted://commands/501", PayloadHash: "command-hash-501"}}}
	result, err := appendAndCommit(ctx, service, appender, base)
	if err != nil || result.Sequence != 1 || result.Replayed {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	replay, err := appendAndCommit(ctx, service, appender, base)
	if err != nil || replay.Sequence != 1 || !replay.Replayed {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	tamperedReplay := base
	tamperedReplay.Commands = append([]OutboxCommand(nil), base.Commands...)
	tamperedReplay.Commands[0].PayloadHash = "different-command-hash"
	if _, err = appendAndCommit(ctx, service, appender, tamperedReplay); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("tampered replay error=%v", err)
	}
	tamperedEvent := base
	tamperedEvent.Event.Actor = json.RawMessage(`{"kind":"system","id":"different-service"}`)
	if _, err = appendAndCommit(ctx, service, appender, tamperedEvent); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("tampered event replay error=%v", err)
	}

	second := versionedInput(base, 2, 502)
	result, err = appendAndCommit(ctx, service, appender, second)
	if err != nil || result.Sequence != 2 {
		t.Fatalf("second=%#v err=%v", result, err)
	}

	contenders := []Input{versionedInput(base, 3, 503), versionedInput(base, 3, 504)}
	start := make(chan struct{})
	results := make(chan error, len(contenders))
	var wait sync.WaitGroup
	for _, contender := range contenders {
		contender := contender
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := appendAndCommit(ctx, service, appender, contender)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var successes, conflicts int
	for appendErr := range results {
		switch {
		case appendErr == nil:
			successes++
		case errors.Is(appendErr, ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected append error: %v", appendErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	var events, outbox int
	var lastSequence uint64
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id=$2`, tenantID, aggregateID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id=$2`, tenantID, aggregateID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT last_seq FROM agent.event_cursors WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&lastSequence); err != nil {
		t.Fatal(err)
	}
	if events != 3 || outbox != 6 || lastSequence != 3 {
		t.Fatalf("events=%d outbox=%d last-seq=%d", events, outbox, lastSequence)
	}
}

func appendAndCommit(ctx context.Context, pool *pgxpool.Pool, appender Appender, input Input) (Result, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := appender.Append(ctx, tx, input)
	if err != nil {
		return Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result, nil
}

func versionedInput(base Input, version uint64, suffix int) Input {
	value := base
	value.Event.ID = fmt.Sprintf("60000000-0000-0000-0000-%012d", suffix)
	value.Event.AggregateVersion = version
	value.Event.PayloadRef = fmt.Sprintf("encrypted://events/%d", suffix)
	value.Event.PayloadHash = fmt.Sprintf("event-hash-%d", suffix)
	value.Commands = append([]OutboxCommand(nil), base.Commands...)
	value.Commands[0].ID = fmt.Sprintf("70000000-0000-0000-0000-%012d", suffix)
	value.Commands[0].CommandID = fmt.Sprintf("80000000-0000-0000-0000-%012d", suffix)
	value.Commands[0].PayloadRef = fmt.Sprintf("encrypted://commands/%d", suffix)
	value.Commands[0].PayloadHash = fmt.Sprintf("command-hash-%d", suffix)
	value.Commands[1].ID = fmt.Sprintf("71000000-0000-0000-0000-%012d", suffix)
	value.Commands[1].CommandID = fmt.Sprintf("81000000-0000-0000-0000-%012d", suffix)
	value.Commands[1].PayloadRef = fmt.Sprintf("encrypted://mail/%d", suffix)
	value.Commands[1].PayloadHash = fmt.Sprintf("mail-hash-%d", suffix)
	return value
}

func poolFromEnv(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("missing %s", name)
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
