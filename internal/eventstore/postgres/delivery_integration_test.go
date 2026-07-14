//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestOutboxAndInboxLeasesFenceStaleDelivery(t *testing.T) {
	ctx := context.Background()
	admin := deliveryPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := deliveryPool(t, ctx, "LITES_TEST_IDENTITY_DATABASE_URL")
	defer pool.Close()
	const tenantID = "20000000-0000-0000-0000-000000009901"
	const epoch = "40000000-0000-0000-0000-000000009901"
	const outboxID = "50000000-0000-0000-0000-000000009901"
	const commandID = "60000000-0000-0000-0000-000000009901"
	const aggregateID = "70000000-0000-0000-0000-000000009901"
	availableAt := time.Now().UTC().Add(-time.Minute)
	now := availableAt.Add(2 * time.Minute)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'enterprise','Delivery Tenant','active','US')`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO agent.outbox (id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at) VALUES ($1,$2,$3,'identity.invitation_import.process','invitation_import',$4,$5,'blob://command','payload-hash','pending',$6)`, outboxID, tenantID, commandID, aggregateID, epoch, availableAt); err != nil {
		t.Fatal(err)
	}
	authority := epochAuthorityStub{epoch: epoch}
	tokens := opaque.Manager{Purpose: "delivery-lease", Pepper: bytes.Repeat([]byte{0x71}, 32)}
	outbox := OutboxStore{Pool: pool, Epochs: authority, Tokens: tokens, LeaseTTL: time.Minute, RetryBase: time.Second, RetryLimit: time.Minute, Now: func() time.Time { return now }}
	tenantIDs, err := outbox.ListReadyTenantIDs(ctx, epoch, "", 5000, 0, 1)
	if err != nil || !containsTenantID(tenantIDs, tenantID) {
		t.Fatalf("tenant catalog ids=%v error=%v", tenantIDs, err)
	}
	shardOccurrences := 0
	for shard := 0; shard < 2; shard++ {
		shardIDs, shardErr := outbox.ListReadyTenantIDs(ctx, epoch, "", 5000, shard, 2)
		if shardErr != nil {
			t.Fatal(shardErr)
		}
		if containsTenantID(shardIDs, tenantID) {
			shardOccurrences++
		}
	}
	if shardOccurrences != 1 {
		t.Fatalf("tenant appeared in %d publisher shards", shardOccurrences)
	}
	if _, err := outbox.ClaimBatch(ctx, tenantID, "40000000-0000-0000-0000-000000009999", 1); !errors.Is(err, ErrStaleStoreEpoch) {
		t.Fatalf("stale epoch claim=%v", err)
	}
	claims, err := outbox.ClaimBatch(ctx, tenantID, epoch, 10)
	if err != nil || len(claims) != 1 || claims[0].PublishAttempts != 1 {
		t.Fatalf("claims=%#v error=%v", claims, err)
	}
	if duplicate, claimErr := outbox.ClaimBatch(ctx, tenantID, epoch, 10); claimErr != nil || len(duplicate) != 0 {
		t.Fatalf("active lease duplicate=%#v error=%v", duplicate, claimErr)
	}
	first := claims[0]
	if err = outbox.Defer(ctx, first); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	claims, err = outbox.ClaimBatch(ctx, tenantID, epoch, 10)
	if err != nil || len(claims) != 1 || claims[0].PublishAttempts != 2 {
		t.Fatalf("reclaims=%#v error=%v", claims, err)
	}
	if err = outbox.MarkPublished(ctx, first); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("stale publisher completion=%v", err)
	}
	if err = outbox.MarkPublished(ctx, claims[0]); err != nil {
		t.Fatal(err)
	}
	var outboxStatus string
	var attempts int
	if err = admin.QueryRow(ctx, `SELECT status,publish_attempts FROM agent.outbox WHERE id=$1`, outboxID).Scan(&outboxStatus, &attempts); err != nil || outboxStatus != "published" || attempts != 2 {
		t.Fatalf("outbox status=%s attempts=%d error=%v", outboxStatus, attempts, err)
	}

	inbox := InboxStore{Pool: pool, Epochs: authority, Tokens: tokens, LeaseTTL: time.Minute, Now: func() time.Time { return now }}
	command := DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: commandID, CommandType: "identity.invitation_import.process", AggregateKind: "invitation_import", AggregateID: aggregateID, PayloadRef: "blob://command", PayloadHash: "payload-hash"}
	claim, err := inbox.Claim(ctx, "identity-import-worker", command)
	if err != nil || claim.Fence != 1 || claim.Completed || claim.Busy {
		t.Fatalf("inbox claim=%#v error=%v", claim, err)
	}
	busy, err := inbox.Claim(ctx, "identity-import-worker", command)
	if !errors.Is(err, ErrDeliveryBusy) || !busy.Busy || busy.Fence != 1 {
		t.Fatalf("busy claim=%#v error=%v", busy, err)
	}
	tampered := command
	tampered.PayloadHash = "different-hash"
	if _, err = inbox.Claim(ctx, "identity-import-worker", tampered); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("payload conflict=%v", err)
	}
	now = now.Add(2 * time.Minute)
	reclaimed, err := inbox.Claim(ctx, "identity-import-worker", command)
	if err != nil || reclaimed.Fence != 2 || reclaimed.AttemptID == claim.AttemptID {
		t.Fatalf("reclaimed=%#v error=%v", reclaimed, err)
	}
	if err = inbox.Complete(ctx, claim); !errors.Is(err, ErrDeliveryConflict) {
		t.Fatalf("stale inbox completion=%v", err)
	}
	if err = inbox.Complete(ctx, reclaimed); err != nil {
		t.Fatal(err)
	}
	completed, err := inbox.Claim(ctx, "identity-import-worker", command)
	if err != nil || !completed.Completed || completed.Fence != 2 {
		t.Fatalf("completed replay=%#v error=%v", completed, err)
	}
	var inboxStatus string
	var fence int
	if err = admin.QueryRow(ctx, `SELECT status,fence FROM agent.inbox WHERE tenant_id=$1 AND consumer_name='identity-import-worker' AND command_id=$2`, tenantID, commandID).Scan(&inboxStatus, &fence); err != nil || inboxStatus != "completed" || fence != 2 {
		t.Fatalf("inbox status=%s fence=%d error=%v", inboxStatus, fence, err)
	}
}

func containsTenantID(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func deliveryPool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
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
