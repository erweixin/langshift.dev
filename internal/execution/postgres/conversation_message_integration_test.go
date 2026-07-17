//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langshift/lites/internal/behavior"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

func TestAcceptMessageRunIsAtomicReplaySafeAndVersionFenced(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()

	const tenantID = "30000000-0000-4000-8000-000000000001"
	const userID = "30000000-0000-4000-8000-000000000002"
	const roleID = "30000000-0000-4000-8000-000000000003"
	const missionID = "30000000-0000-4000-8000-000000000004"
	const conversationID = "30000000-0000-4000-8000-000000000005"
	const messageID = "30000000-0000-4000-8000-000000000006"
	const runID = "30000000-0000-4000-8000-000000000007"
	const correlationID = "30000000-0000-4000-8000-000000000008"
	const storeEpoch = "30000000-0000-4000-8000-000000000009"
	const losingMessageID = "30000000-0000-4000-8000-000000000010"
	const losingRunID = "30000000-0000-4000-8000-000000000011"
	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)

	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'message-run-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Message Run Owner','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'message-run-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'message-run-claims')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.mission_focuses(tenant_id,user_id,mission_id,focus_version) VALUES($1,$2,$3,1)`, []any{tenantID, userID, missionID}},
	}
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	seedExecutionBehavior(t, ctx, admin, tenantID, userID, behavior.Coach, now)
	store := RunStore{
		Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }},
		IDKey: bytes.Repeat([]byte{0x73}, 32), StoreEpoch: storeEpoch,
		Now: func() time.Time { return now }, Behavior: integrationBehaviorResolver,
	}
	actor := json.RawMessage(`{"kind":"user","id":"30000000-0000-4000-8000-000000000002"}`)
	if _, err := store.CreateConversation(ctx, CreateConversationCommand{
		ConversationID: conversationID, TenantID: tenantID, UserID: userID, MissionID: missionID,
		Mode: "coach", CorrelationID: correlationID, Actor: actor,
		CreatedEvent: integrationMessagePointer("conversation-created"),
	}); err != nil {
		t.Fatal(err)
	}
	command := AcceptMessageRunCommand{
		Run: AcceptRunCommand{
			RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID,
			DueAt: now.Add(time.Hour), BehaviorProfile: behavior.Coach, BehaviorEnvironment: "production",
			BudgetSnapshot: json.RawMessage(`{"max_steps":32,"max_cost_microunits":100000}`), Actor: actor,
			AcceptedEvent: integrationMessagePointer("run-accepted"), QueuedEvent: integrationMessagePointer("run-queued"), StartCommand: integrationMessagePointer("run-start"),
			QueueClass: "interactive", ResourceClass: "llm", Priority: 100, CostUnits: 32, MaxAttempts: 5,
		},
		MessageID: messageID, ExpectedConversationVersion: 1, ExpectedConversationMode: "coach", ExpectedConversationProfile: behavior.Coach,
		Message: integrationMessagePointer("user-message"), ContentHash: integrationMessageHash("user-message-content"),
		AppendedEvent: integrationMessagePointer("message-appended"), Actor: actor,
	}

	const contenders = 16
	var wait sync.WaitGroup
	var replayed atomic.Int64
	errorsFound := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.AcceptMessageRun(ctx, command)
			if err != nil {
				errorsFound <- err
				return
			}
			if result.RunID != runID || result.MessageID != messageID || result.RunVersion != 2 || result.ConversationVersion != 2 || result.Status != "queued" {
				errorsFound <- errors.New("non-convergent Message-to-Run result")
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

	var version uint64
	var lastRun string
	var messages, events, outbox, jobs int
	if err := admin.QueryRow(ctx, `
		SELECT
		  (SELECT version FROM agent.conversations WHERE tenant_id=$1 AND id=$2),
		  (SELECT last_run_id::text FROM agent.conversations WHERE tenant_id=$1 AND id=$2),
		  (SELECT count(*) FROM agent.run_messages WHERE tenant_id=$1 AND run_id=$3 AND role='user'),
		  (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND ((aggregate_kind='conversation' AND aggregate_id=$2) OR (aggregate_kind='run' AND aggregate_id=$3))),
		  (SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND ((aggregate_kind='conversation' AND aggregate_id=$2) OR (aggregate_kind='run' AND aggregate_id=$3))),
		  (SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND command_id=$4)`,
		tenantID, conversationID, runID, mustMessageRunIdentifiers(t, store, runID).startCommand,
	).Scan(&version, &lastRun, &messages, &events, &outbox, &jobs); err != nil {
		t.Fatal(err)
	}
	if version != 2 || lastRun != runID || messages != 1 || events != 4 || outbox != 5 || jobs != 1 {
		t.Fatalf("version=%d last_run=%s messages=%d events=%d outbox=%d jobs=%d", version, lastRun, messages, events, outbox, jobs)
	}

	loser := command
	loser.MessageID = losingMessageID
	loser.Run.RunID = losingRunID
	loser.Message = integrationMessagePointer("losing-message")
	loser.ContentHash = integrationMessageHash("losing-message-content")
	loser.AppendedEvent = integrationMessagePointer("losing-message-appended")
	loser.Run.AcceptedEvent = integrationMessagePointer("losing-run-accepted")
	loser.Run.QueuedEvent = integrationMessagePointer("losing-run-queued")
	loser.Run.StartCommand = integrationMessagePointer("losing-run-start")
	if _, err := store.AcceptMessageRun(ctx, loser); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("stale Conversation version error=%v", err)
	}
	var partialRuns, partialMessages, partialEvents, partialOutbox int
	if err := admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=$2),
		(SELECT count(*) FROM agent.run_messages WHERE tenant_id=$1 AND id=$3),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id IN ($2,$3)),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id IN ($2,$3))`,
		tenantID, losingRunID, losingMessageID,
	).Scan(&partialRuns, &partialMessages, &partialEvents, &partialOutbox); err != nil {
		t.Fatal(err)
	}
	if partialRuns != 0 || partialMessages != 0 || partialEvents != 0 || partialOutbox != 0 {
		t.Fatalf("losing admission left partial state: runs=%d messages=%d events=%d outbox=%d", partialRuns, partialMessages, partialEvents, partialOutbox)
	}
}

func integrationMessageHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func integrationMessagePointer(value string) PayloadPointer {
	return PayloadPointer{Ref: "encrypted://integration/" + value, Hash: integrationMessageHash(value)}
}

func mustMessageRunIdentifiers(t *testing.T, store RunStore, runID string) runIdentifiers {
	t.Helper()
	identifiers, err := store.identifiers(runID)
	if err != nil {
		t.Fatal(err)
	}
	return identifiers
}
