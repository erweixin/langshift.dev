//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

func TestConcurrentReservationsEnforceHardCreditCapAndReplay(t *testing.T) {
	ctx := context.Background()
	admin := billingPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := billingPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 15, 22, 0, 0, 0, time.UTC)
	const (
		userID   = "d7000000-0000-4000-8000-000000000001"
		tenantID = "d7000000-0000-4000-8000-000000000002"
		bucketID = "d7000000-0000-4000-8000-000000000003"
		epoch    = "d7000000-0000-4000-8000-000000000004"
	)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'billing-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Billing Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO contracts.credit_buckets(id,tenant_id,bucket_kind,granted_units,starts_at,expires_at,created_at,updated_at) VALUES($1,$2,'llm',1000,$3,$4,$5,$5)`, []any{bucketID, tenantID, now.Add(-time.Hour), now.Add(24 * time.Hour), now}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE contracts.credit_buckets SET reserved_units=1 WHERE tenant_id=$1 AND id=$2`, tenantID, bucketID); !hasSQLState(err, "42501") {
		t.Fatalf("agent role direct bucket mutation err=%v", err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	var ignored uint64
	err = tx.QueryRow(ctx, `SELECT contracts.reserve_credit_units($1,$2,1,$3,$4)`, "d7000000-0000-4000-8000-000000000099", bucketID, now, now.Add(time.Hour)).Scan(&ignored)
	if !hasSQLState(err, "42501") {
		t.Fatalf("cross-tenant privileged reservation err=%v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	store := Store{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, Epochs: billingEpochStub{epoch}, StoreEpoch: epoch, IDKey: bytes.Repeat([]byte{0xd7}, 32), Now: func() time.Time { return now }}
	const contenders = 20
	commands := make([]ReserveCommand, contenders)
	var wait sync.WaitGroup
	errorsFound := make(chan error, contenders)
	for index := range contenders {
		id := fmt.Sprintf("d7000000-0000-4000-8000-%012d", 100+index)
		commands[index] = ReserveCommand{ReservationID: id, RequestID: fmt.Sprintf("request-%d", index), TenantID: tenantID, UserID: userID, BucketID: bucketID, OperationKey: fmt.Sprintf("provider-%d", index), SubjectKind: "provider_attempt", SubjectID: fmt.Sprintf("d7000000-0000-4000-9000-%012d", 100+index), SubjectVersion: 1, ReservedUnits: 100, ExpiresAt: now.Add(time.Hour), CorrelationID: id, Actor: json.RawMessage(`{"kind":"service"}`), ReservedEvent: PayloadPointer{Ref: "encrypted://usage/" + id, Hash: "hash-" + id}}
		wait.Add(1)
		go func(command ReserveCommand) {
			defer wait.Done()
			_, err := store.Reserve(ctx, command)
			errorsFound <- err
		}(commands[index])
	}
	wait.Wait()
	close(errorsFound)
	succeeded, exhausted := 0, 0
	for err := range errorsFound {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrInsufficientCredits) {
			exhausted++
		} else {
			t.Fatalf("reserve error: %v", err)
		}
	}
	if succeeded != 10 || exhausted != 10 {
		t.Fatalf("succeeded=%d exhausted=%d", succeeded, exhausted)
	}
	var reserved, settled uint64
	var reservations, events int
	if err := admin.QueryRow(ctx, `SELECT reserved_units,settled_units,(SELECT count(*) FROM contracts.usage_reservations WHERE tenant_id=$2),(SELECT count(*) FROM agent.events WHERE tenant_id=$2 AND event_type='UsageReserved') FROM contracts.credit_buckets WHERE id=$1`, bucketID, tenantID).Scan(&reserved, &settled, &reservations, &events); err != nil {
		t.Fatal(err)
	}
	if reserved != 1000 || settled != 0 || reservations != 10 || events != 10 {
		t.Fatalf("reserved=%d settled=%d reservations=%d events=%d", reserved, settled, reservations, events)
	}
	var winner ReserveCommand
	for _, command := range commands {
		result, err := store.Reserve(ctx, command)
		if err == nil {
			winner = command
			if !result.Replayed {
				t.Fatal("durable reservation replay not marked")
			}
			break
		}
		if !errors.Is(err, ErrInsufficientCredits) {
			t.Fatal(err)
		}
	}
	if winner.ReservationID == "" {
		t.Fatal("no winning reservation replay found")
	}
}

func hasSQLState(err error, state string) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == state
}

type billingEpochStub struct{ epoch string }

func (stub billingEpochStub) CurrentStoreEpoch(context.Context) (string, error) {
	return stub.epoch, nil
}
func billingPool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
