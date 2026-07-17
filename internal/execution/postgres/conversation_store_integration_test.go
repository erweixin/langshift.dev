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

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

func TestCreateConversationIsAtomicReplaySafeAndTenantIsolated(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	const tenantID = "2f000000-0000-4000-8000-000000000001"
	const otherTenantID = "2f000000-0000-4000-8000-000000000002"
	const userID = "2f000000-0000-4000-8000-000000000003"
	const roleID = "2f000000-0000-4000-8000-000000000004"
	const missionID = "2f000000-0000-4000-8000-000000000005"
	const conversationID = "2f000000-0000-4000-8000-000000000006"
	const correlationID = "2f000000-0000-4000-8000-000000000007"
	const storeEpoch = "2f000000-0000-4000-8000-000000000008"
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'conversation-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Conversation Owner','active','US',$3),($2,'enterprise','Other Tenant','active','US',NULL)`, []any{tenantID, otherTenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'conversation-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'conversation-claims')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.mission_focuses(tenant_id,user_id,mission_id,focus_version) VALUES($1,$2,$3,1)`, []any{tenantID, userID, missionID}},
	}
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	title := "Career transition"
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x72}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }}
	command := CreateConversationCommand{
		ConversationID: conversationID, TenantID: tenantID, UserID: userID, MissionID: missionID,
		Title: &title, Mode: "coach", CorrelationID: correlationID,
		Actor:        json.RawMessage(`{"kind":"user","id":"2f000000-0000-4000-8000-000000000003"}`),
		CreatedEvent: PayloadPointer{Ref: "encrypted://conversation/created", Hash: "conversation-created-hash"},
	}
	const contenders = 16
	var wait sync.WaitGroup
	var replayed atomic.Int64
	errorsFound := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.CreateConversation(ctx, command)
			if err != nil {
				errorsFound <- err
				return
			}
			if result.ID != conversationID || result.Version != 1 || result.Status != "active" || result.EventID == "" {
				errorsFound <- errors.New("non-convergent conversation result")
				return
			}
			if result.Replayed {
				replayed.Add(1)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	if replayed.Load() != contenders-1 {
		t.Fatalf("replayed=%d want=%d", replayed.Load(), contenders-1)
	}
	var conversations, events, outbox int
	if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.conversations WHERE tenant_id=$1 AND id=$2),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='conversation' AND aggregate_id=$2 AND event_type='ConversationCreated'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_kind='conversation' AND aggregate_id=$2 AND command_type='events.publish')`, tenantID, conversationID).Scan(&conversations, &events, &outbox); err != nil {
		t.Fatal(err)
	}
	if conversations != 1 || events != 1 || outbox != 1 {
		t.Fatalf("conversation=%d events=%d outbox=%d", conversations, events, outbox)
	}
	conflict := command
	conflictingTitle := "Different intent"
	conflict.Title = &conflictingTitle
	if _, err := store.CreateConversation(ctx, conflict); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, otherTenantID); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.conversations WHERE id=$1`, conversationID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("cross-tenant visible=%d error=%v", visible, err)
	}
}
