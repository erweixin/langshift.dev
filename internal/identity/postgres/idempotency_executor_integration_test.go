//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/idempotency"
)

func TestIdempotencyExecutorCommitsOneEffectAndReplaysNinetyNineResponses(t *testing.T) {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	servicePool, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_IDENTITY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer servicePool.Close()
	const userID = "10000000-0000-0000-0000-000000000301"
	const tenantID = "20000000-0000-0000-0000-000000000301"
	const otherTenantID = "20000000-0000-0000-0000-000000000302"
	const eventID = "50000000-0000-0000-0000-000000000301"
	const recordID = "60000000-0000-0000-0000-000000000301"
	now := time.Unix(1_800_000_200, 0).UTC()
	if _, err = admin.Exec(ctx, `INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,'idempotency@lites.invalid',$2,'en','active')`, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Idempotency','active','US',$2),($3,'anonymous_system','Public','active','US',NULL)`, tenantID, userID, otherTenantID); err != nil {
		t.Fatal(err)
	}
	executor := IdempotencyExecutor{Pool: servicePool, KeyPepper: bytes.Repeat([]byte{0x61}, 32), TTL: 24 * time.Hour, Now: func() time.Time { return now }}
	input := IdempotencyInput{RecordID: recordID, Scope: idempotency.Scope{TenantID: tenantID, UserID: userID, OperationID: "account.update"}, RawKey: "idempotency-key-0000001", RequestHash: idempotency.RequestHash([]byte(`{"locale":"en"}`)), RequestID: "request-idempotency-1"}
	expected := idempotency.Response{Status: 200, ContentType: "application/json", PayloadRef: "encrypted://responses/response-301", Hash: idempotency.RequestHash([]byte(`{"id":"user"}`)), ResourceVersion: 2}
	var effects atomic.Uint64
	const requests = 100
	var wait sync.WaitGroup
	start := make(chan struct{})
	results := make(chan bool, requests)
	failures := make(chan error, requests)
	for range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
				effects.Add(1)
				_, err := tx.Exec(ctx, `INSERT INTO identity.security_events (id,tenant_id,subject_user_id,actor_user_id,event_type,request_id,details,occurred_at) VALUES ($1,$2,$3,$3,'idempotency_effect',$4,'{}',$5)`, eventID, tenantID, userID, input.RequestID, now)
				return expected, err
			})
			if err != nil {
				failures <- err
				return
			}
			if response != expected {
				failures <- errors.New("replayed response differs")
			}
			results <- replayed
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(failures)
	for failure := range failures {
		t.Errorf("execute: %v", failure)
	}
	var replayed int
	for value := range results {
		if value {
			replayed++
		}
	}
	if effects.Load() != 1 || replayed != 99 {
		t.Fatalf("effects=%d replayed=%d", effects.Load(), replayed)
	}
	conflict := input
	conflict.RecordID = "60000000-0000-0000-0000-000000000302"
	conflict.RequestHash = idempotency.RequestHash([]byte(`{"locale":"zh-CN"}`))
	if _, _, err = executor.Execute(ctx, conflict, func(context.Context, pgx.Tx) (idempotency.Response, error) {
		t.Fatal("conflicting request executed")
		return expected, nil
	}); !errors.Is(err, idempotency.ErrKeyConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	var rows, events int
	var storedDigest []byte
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT idempotency_key_hash FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.security_events WHERE id=$1`, eventID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || events != 1 || bytes.Contains(storedDigest, []byte(input.RawKey)) {
		t.Fatalf("rows=%d events=%d raw-key-leaked=%v", rows, events, bytes.Contains(storedDigest, []byte(input.RawKey)))
	}

	switching := input
	switching.RecordID = "60000000-0000-0000-0000-000000000303"
	switching.RawKey = "idempotency-key-tenant-switch"
	switching.RequestID = "request-idempotency-tenant-switch"
	if _, _, err = executor.Execute(ctx, switching, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		_, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, otherTenantID)
		return expected, err
	}); err != nil {
		t.Fatalf("tenant-switching mutation: %v", err)
	}
	var switchingStatus string
	if err = admin.QueryRow(ctx, `SELECT status FROM agent.idempotency_responses WHERE id=$1 AND tenant_id=$2`, switching.RecordID, tenantID).Scan(&switchingStatus); err != nil || switchingStatus != "completed" {
		t.Fatalf("switching status=%q err=%v", switchingStatus, err)
	}
}
