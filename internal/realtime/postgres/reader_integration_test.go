//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReaderEnforcesTenantAndUserScopedReadOnlyBackfill(t *testing.T) {
	ctx := context.Background()
	admin := realtimePool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	service := realtimePool(t, ctx, "LITES_TEST_REALTIME_DATABASE_URL")
	defer service.Close()

	const (
		userA     = "10000000-0000-0000-0000-000000009001"
		userA2    = "10000000-0000-0000-0000-000000009002"
		userB     = "10000000-0000-0000-0000-000000009003"
		tenantA   = "20000000-0000-0000-0000-000000009001"
		tenantB   = "20000000-0000-0000-0000-000000009002"
		epoch     = "40000000-0000-0000-0000-000000009001"
		correlate = "50000000-0000-0000-0000-000000009001"
	)
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES ($1,'realtime-a@lites.invalid',$4,'en','active'),($2,'realtime-a2@lites.invalid',$4,'en','active'),($3,'realtime-b@lites.invalid',$4,'en','active')`, userA, userA2, userB, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Realtime A','active','US',$3),($2,'personal','Realtime B','active','US',$4)`, tenantA, tenantB, userA, userB); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO agent.event_cursors(tenant_id,user_id,last_seq) VALUES ($1,$3,2),($1,$4,1),($2,$5,1)`, tenantA, tenantB, userA, userA2, userB); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES
		('60000000-0000-0000-0000-000000009001',$1,$3,1,'RunQueued',1,'run','30000000-0000-0000-0000-000000009001',1,$6,$8,$8,'{"kind":"system"}',$7,'encrypted://event/a1','hash-a1'),
		('60000000-0000-0000-0000-000000009002',$1,$3,2,'RunStarted',1,'run','30000000-0000-0000-0000-000000009001',2,$6,$8,$8,'{"kind":"system"}',$7,'encrypted://event/a2','hash-a2'),
		('60000000-0000-0000-0000-000000009003',$1,$4,1,'RunQueued',1,'run','30000000-0000-0000-0000-000000009002',1,$6,$8,$8,'{"kind":"system"}',$7,'encrypted://event/a-other','hash-a-other'),
		('60000000-0000-0000-0000-000000009004',$2,$5,1,'RunQueued',1,'run','30000000-0000-0000-0000-000000009003',1,$6,$8,$8,'{"kind":"system"}',$7,'encrypted://event/b1','hash-b1')`, tenantA, tenantB, userA, userA2, userB, epoch, correlate, now); err != nil {
		t.Fatal(err)
	}

	reader := Reader{Pool: service}
	high, err := reader.HighWatermark(ctx, tenantA, userA)
	if err != nil || high != 2 {
		t.Fatalf("high watermark = %d, error = %v", high, err)
	}
	events, err := reader.List(ctx, tenantA, userA, 0, high, 100)
	if err != nil || len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 || events[0].PayloadRef != "encrypted://event/a1" {
		t.Fatalf("events = %#v, error = %v", events, err)
	}
	otherUser, err := reader.List(ctx, tenantA, userA2, 0, 1, 100)
	if err != nil || len(otherUser) != 1 || otherUser[0].AggregateID != "30000000-0000-0000-0000-000000009002" {
		t.Fatalf("other user events = %#v, error = %v", otherUser, err)
	}
	tenantBEvents, err := reader.List(ctx, tenantB, userB, 0, 1, 100)
	if err != nil || len(tenantBEvents) != 1 || tenantBEvents[0].AggregateID != "30000000-0000-0000-0000-000000009003" {
		t.Fatalf("tenant B events = %#v, error = %v", tenantBEvents, err)
	}

	// The dedicated service role cannot mutate the append-only ledger even
	// if application code is compromised.
	if _, err = service.Exec(ctx, `UPDATE agent.event_cursors SET last_seq=999 WHERE tenant_id=$1`, tenantA); err == nil {
		t.Fatal("realtime role unexpectedly mutated an event cursor")
	}
}

func realtimePool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
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
