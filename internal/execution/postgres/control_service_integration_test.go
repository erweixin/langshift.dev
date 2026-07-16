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

	"github.com/langshift/lites/internal/behavior"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionapi "github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestAgentControlServiceEncryptsAndAtomicallyReplaysConversationMessageRun(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()

	const tenantID = "31000000-0000-4000-8000-000000000001"
	const userID = "31000000-0000-4000-8000-000000000002"
	const roleID = "31000000-0000-4000-8000-000000000003"
	const missionID = "31000000-0000-4000-8000-000000000004"
	const sessionID = "31000000-0000-4000-8000-000000000005"
	const requestID = "31000000-0000-4000-8000-000000000006"
	const storeEpoch = "31000000-0000-4000-8000-000000000007"
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'control-service-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Control Service Owner','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'control-service-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'control-service-claims')`, []any{missionID, tenantID, userID, roleID}},
	}
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	binding := seedExecutionBehavior(t, ctx, admin, tenantID, userID, behavior.RoutePlanner, now)
	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloads := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "agent-control-v1", Material: bytes.Repeat([]byte{0x75}, 32)}}, Blobs: blobs}
	store := RunStore{
		Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }},
		IDKey: bytes.Repeat([]byte{0x76}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Behavior: integrationBehaviorResolver,
		Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "agent-control-cancellation", Pepper: bytes.Repeat([]byte{0x79}, 32)}, LeaseTTL: 2 * time.Minute,
	}
	service := ControlService{
		Pool: pool, Store: store, Payloads: payloads, IDKey: store.IDKey,
		IdempotencyKeyPepper: bytes.Repeat([]byte{0x77}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x78}, 32),
		IdempotencyTTL: 24 * time.Hour, RunTimeout: time.Hour, RunMaxSteps: 32, RunMaxCostMicrounits: 100000, RunMaxAttempts: 5,
		Now: func() time.Time { return now },
	}
	create := executionapi.CreateConversationCommand{
		ControlMetadata: executionapi.ControlMetadata{RequestID: requestID, ClientRequestID: "client-conversation-0001", IdempotencyKey: "conversation-key-0000001", TenantID: tenantID, UserID: userID, SessionID: sessionID},
		MissionID:       missionID, Mode: "task",
	}
	conversation, err := service.CreateConversation(ctx, create)
	if err != nil || conversation.ID == "" || conversation.Version != 1 || conversation.Status != "active" || !conversation.UpdatedAt.Equal(now) {
		t.Fatalf("conversation=%#v error=%v", conversation, err)
	}
	createConflict := create
	createConflict.Mode = "project"
	if _, err = service.CreateConversation(ctx, createConflict); !errors.Is(err, executionapi.ErrIdempotencyConflict) {
		t.Fatalf("Conversation idempotency mismatch error=%v", err)
	}

	const secretContent = "Private career plan: acquire the target role without exposing this text."
	message := executionapi.CreateMessageCommand{
		ControlMetadata: executionapi.ControlMetadata{RequestID: requestID, ClientRequestID: "client-message-000000001", IdempotencyKey: "message-key-0000000001", TenantID: tenantID, UserID: userID, SessionID: sessionID},
		ConversationID:  conversation.ID, Content: secretContent, Mode: "enqueue", ExpectedConversationVersion: 1,
	}
	const contenders = 16
	var wait sync.WaitGroup
	var failures atomic.Int64
	results := make(chan executionapi.MessageResult, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, callErr := service.CreateMessage(ctx, message)
			if callErr != nil {
				failures.Add(1)
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	if failures.Load() != 0 {
		t.Fatalf("concurrent Message failures=%d", failures.Load())
	}
	var first executionapi.MessageResult
	for result := range results {
		if first.RunID == "" {
			first = result
		}
		if result != first || result.Status != "queued" || result.ConversationVersion != 2 || !result.AcceptedAt.Equal(now) {
			t.Fatalf("non-convergent Message response=%#v first=%#v", result, first)
		}
	}
	read, err := service.GetRun(ctx, executionapi.GetRunCommand{RequestID: requestID, TenantID: tenantID, UserID: userID, RunID: first.RunID})
	if err != nil || read.ID != first.RunID || read.Version != 2 || read.Status != "queued" || !read.UpdatedAt.Equal(now) {
		t.Fatalf("Run read=%#v error=%v", read, err)
	}
	cancel := executionapi.CancelRunCommand{
		ControlMetadata: executionapi.ControlMetadata{RequestID: requestID, ClientRequestID: "client-cancel-0000001", IdempotencyKey: "cancel-key-00000000001", TenantID: tenantID, UserID: userID, SessionID: sessionID},
		RunID:           first.RunID, Reason: "user_changed_direction", ExpectedRunVersion: 2,
	}
	cancelled, err := service.CancelRun(ctx, cancel)
	if err != nil || cancelled.ID != first.RunID || cancelled.Version != 3 || cancelled.Status != "cancelled" || !cancelled.UpdatedAt.Equal(now) {
		t.Fatalf("Run cancellation=%#v error=%v", cancelled, err)
	}
	replayedCancellation, err := service.CancelRun(ctx, cancel)
	if err != nil || replayedCancellation != cancelled {
		t.Fatalf("Run cancellation replay=%#v error=%v", replayedCancellation, err)
	}
	cancelConflict := cancel
	cancelConflict.Reason = "different_reason"
	if _, err = service.CancelRun(ctx, cancelConflict); !errors.Is(err, executionapi.ErrVersionConflict) && !errors.Is(err, executionapi.ErrIdempotencyConflict) {
		t.Fatalf("Run cancellation conflict error=%v", err)
	}

	var conversations, runs, messages, events, outbox, jobs, responses int
	var snapshotID, channelID string
	var sequence uint64
	if err = admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.conversations WHERE tenant_id=$1 AND id=$2),
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=$3),
		(SELECT count(*) FROM agent.run_messages WHERE tenant_id=$1 AND run_id=$3),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id IN ($2,$3)),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id IN ($2,$3)),
		(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND status='cancelled'),
		(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$4 AND status='completed'),
		(SELECT profile_snapshot_id FROM agent.runs WHERE tenant_id=$1 AND id=$3),
		(SELECT behavior_channel_id::text FROM agent.runs WHERE tenant_id=$1 AND id=$3),
		(SELECT behavior_channel_sequence FROM agent.runs WHERE tenant_id=$1 AND id=$3)`,
		tenantID, conversation.ID, first.RunID, userID,
	).Scan(&conversations, &runs, &messages, &events, &outbox, &jobs, &responses, &snapshotID, &channelID, &sequence); err != nil {
		t.Fatal(err)
	}
	if conversations != 1 || runs != 1 || messages != 1 || events != 5 || outbox != 6 || jobs != 1 || responses != 3 || snapshotID != binding.SnapshotID || channelID != binding.ChannelID || sequence != binding.Sequence {
		t.Fatalf("conversation=%d runs=%d messages=%d events=%d outbox=%d jobs=%d responses=%d binding=%s/%s/%d", conversations, runs, messages, events, outbox, jobs, responses, snapshotID, channelID, sequence)
	}
	blobs.mu.RLock()
	defer blobs.mu.RUnlock()
	for ref, encoded := range blobs.values {
		if bytes.Contains(encoded, []byte(secretContent)) {
			t.Fatalf("plaintext Message leaked to object %s", ref)
		}
	}
}
