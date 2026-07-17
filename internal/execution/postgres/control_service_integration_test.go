//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	const otherMissionID = "31000000-0000-4000-8000-000000000014"
	const routeID = "31000000-0000-4000-8000-000000000016"
	const evidenceID = "31000000-0000-4000-8000-000000000017"
	const sessionID = "31000000-0000-4000-8000-000000000005"
	const requestID = "31000000-0000-4000-8000-000000000006"
	const storeEpoch = "31000000-0000-4000-8000-000000000007"
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloads := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "agent-control-v1", Material: bytes.Repeat([]byte{0x75}, 32)}}, Blobs: blobs}
	const goalText = "Become a production cloud agent engineer"
	const routeText = "Bridge through distributed systems and agent reliability"
	const evidenceText = "Designed an idempotent event-driven workflow"
	goalBody := []byte(`{"goal":"` + goalText + `"}`)
	routeBody := []byte(`{"summary":"` + routeText + `"}`)
	evidenceBody := []byte(`{"statement":"` + evidenceText + `"}`)
	goalManifest, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: missionID, Class: "mission-goal", ContentType: "application/json"}, goalBody)
	if err != nil {
		t.Fatal(err)
	}
	routeManifest, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: routeID, Class: "route-revision", ContentType: "application/json"}, routeBody)
	if err != nil {
		t.Fatal(err)
	}
	evidenceManifest, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: evidenceID, Class: "product-evidence", ContentType: "application/json"}, evidenceBody)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'control-service-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Control Service Owner','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'control-service-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,goal_payload_ref,goal_payload_hash,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,$5,$6,1,'control-service-claims')`, []any{missionID, tenantID, userID, roleID, goalManifest.Ref, goalManifest.Hash}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'control-service-other-claims')`, []any{otherMissionID, tenantID, userID, roleID}},
		{`INSERT INTO product.mission_focuses(tenant_id,user_id,mission_id,focus_version) VALUES($1,$2,$3,1)`, []any{tenantID, userID, missionID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,route_payload_hash,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','control-service-claims',0,'{}',$5,$6,'route@control','ontology@control','content@control',$7)`, []any{routeID, tenantID, userID, missionID, routeManifest.Ref, routeManifest.Hash, now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE tenant_id=$2 AND id=$3`, []any{routeID, tenantID, missionID}},
		{`INSERT INTO product.user_preferences(tenant_id,user_id,version,locale,timezone,coach_preferences,updated_at) VALUES($1,$2,4,'zh-CN','Asia/Shanghai','{"schema_version":1,"difficulty":"harder","available_minutes":60,"tone":"socratic","explanation_depth":"deep"}',$3)`, []any{tenantID, userID, now}},
		{`INSERT INTO product.evidence(id,tenant_id,user_id,version,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,1,$4,'project','verified','project',$5,$6,$7)`, []any{evidenceID, tenantID, userID, missionID, evidenceManifest.Ref, evidenceManifest.Hash, now}},
	}
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	binding := seedExecutionBehavior(t, ctx, admin, tenantID, userID, behavior.Coach, now)
	store := RunStore{
		Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }},
		IDKey: bytes.Repeat([]byte{0x76}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Behavior: integrationBehaviorResolver,
		Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "agent-control-cancellation", Pepper: bytes.Repeat([]byte{0x79}, 32)}, LeaseTTL: 2 * time.Minute,
	}
	service := ControlService{
		Pool: pool, Store: store, Payloads: payloads, IDKey: store.IDKey,
		IdempotencyKeyPepper: bytes.Repeat([]byte{0x77}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x78}, 32),
		IdempotencyTTL: 24 * time.Hour, RunTimeout: time.Hour, RunMaxSteps: 32, RunMaxCostMicrounits: 100000, RunMaxAttempts: 5, BehaviorEnvironment: "production",
		Now: func() time.Time { return now },
	}
	create := executionapi.CreateConversationCommand{
		ControlMetadata: executionapi.ControlMetadata{RequestID: requestID, ClientRequestID: "client-conversation-0001", IdempotencyKey: "conversation-key-0000001", TenantID: tenantID, UserID: userID, SessionID: sessionID},
		MissionID:       missionID, Mode: "coach",
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
	notFocusedCreate := create
	notFocusedCreate.MissionID = otherMissionID
	notFocusedCreate.ClientRequestID = "client-conversation-0002"
	notFocusedCreate.IdempotencyKey = "conversation-key-0000002"
	if _, err = service.CreateConversation(ctx, notFocusedCreate); !errors.Is(err, executionapi.ErrVersionConflict) {
		t.Fatalf("non-Focus Coach Conversation error=%v", err)
	}

	const secretContent = "Private career plan: acquire the target role without exposing this text."
	message := executionapi.CreateMessageCommand{
		ControlMetadata: executionapi.ControlMetadata{RequestID: requestID, ClientRequestID: "client-message-000000001", IdempotencyKey: "message-key-0000000001", TenantID: tenantID, UserID: userID, SessionID: sessionID},
		ConversationID:  conversation.ID, Content: secretContent, Mode: "enqueue", ExpectedConversationVersion: 1,
	}
	if _, err = admin.Exec(ctx, `UPDATE product.mission_focuses SET mission_id=$1,focus_version=2 WHERE tenant_id=$2 AND user_id=$3`, otherMissionID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	unfocusedMessage := message
	unfocusedMessage.ClientRequestID = "client-message-000000002"
	unfocusedMessage.IdempotencyKey = "message-key-0000000002"
	if _, err = service.CreateMessage(ctx, unfocusedMessage); !errors.Is(err, executionapi.ErrVersionConflict) {
		t.Fatalf("stale-Focus Coach Message error=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE product.mission_focuses SET mission_id=$1,focus_version=3 WHERE tenant_id=$2 AND user_id=$3`, missionID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.prepareCoachContext(ctx, store, tenantID, userID, conversation.ID, "31000000-0000-4000-8000-000000000015"); err != nil {
		t.Fatalf("prepare Coach context: %v", err)
	}
	const contenders = 16
	var wait sync.WaitGroup
	var failures atomic.Int64
	failureErrors := make(chan error, contenders)
	results := make(chan executionapi.MessageResult, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, callErr := service.CreateMessage(ctx, message)
			if callErr != nil {
				failures.Add(1)
				failureErrors <- callErr
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(failureErrors)
	if failures.Load() != 0 {
		t.Fatalf("concurrent Message failures=%d first_error=%v", failures.Load(), <-failureErrors)
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
	conversationPage, err := service.GetConversation(ctx, executionapi.GetConversationCommand{RequestID: requestID, TenantID: tenantID, UserID: userID, ConversationID: conversation.ID, Limit: 50})
	if err != nil || conversationPage.ID != conversation.ID || conversationPage.MissionID != missionID || conversationPage.Version != 2 || len(conversationPage.Messages) != 1 || conversationPage.Messages[0].Role != "user" || conversationPage.Messages[0].Content != secretContent || conversationPage.NextCursor != nil {
		t.Fatalf("Conversation read=%#v error=%v", conversationPage, err)
	}
	if _, err = service.GetConversation(ctx, executionapi.GetConversationCommand{RequestID: requestID, TenantID: tenantID, UserID: otherMissionID, ConversationID: conversation.ID, Limit: 50}); !errors.Is(err, executionapi.ErrResourceNotFound) {
		t.Fatalf("cross-owner Conversation read error=%v", err)
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

	var conversations, runs, messages, events, outbox, jobs, responses, contextSnapshots, contextEvents, contextOutbox int
	var snapshotID, channelID, profile, environment, contextID, contextRef, contextHash, contextRouteID string
	var sequence, contextFocusVersion, contextPreferencesVersion uint64
	if err = admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.conversations WHERE tenant_id=$1 AND id=$2),
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=$3),
		(SELECT count(*) FROM agent.run_messages WHERE tenant_id=$1 AND run_id=$3),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id IN ($2,$3)),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id IN ($2,$3)),
		(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1 AND status='cancelled'),
		(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$4 AND status='completed'),
		(SELECT count(*) FROM agent.coach_context_snapshots WHERE tenant_id=$1 AND run_id=$3),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type='CoachContextSnapshotted' AND aggregate_kind='coach_context'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_kind='coach_context'),
		(SELECT id::text FROM agent.coach_context_snapshots WHERE tenant_id=$1 AND run_id=$3),
		(SELECT payload_ref FROM agent.coach_context_snapshots WHERE tenant_id=$1 AND run_id=$3),
		(SELECT payload_hash FROM agent.coach_context_snapshots WHERE tenant_id=$1 AND run_id=$3),
		(SELECT focus_version FROM agent.coach_context_snapshots WHERE tenant_id=$1 AND run_id=$3),
		(SELECT preferences_version FROM agent.coach_context_snapshots WHERE tenant_id=$1 AND run_id=$3),
		(SELECT route_revision_id::text FROM agent.coach_context_snapshots WHERE tenant_id=$1 AND run_id=$3),
		(SELECT profile_snapshot_id FROM agent.runs WHERE tenant_id=$1 AND id=$3),
		(SELECT behavior_profile FROM agent.runs WHERE tenant_id=$1 AND id=$3),
		(SELECT behavior_environment FROM agent.runs WHERE tenant_id=$1 AND id=$3),
		(SELECT behavior_channel_id::text FROM agent.runs WHERE tenant_id=$1 AND id=$3),
		(SELECT behavior_channel_sequence FROM agent.runs WHERE tenant_id=$1 AND id=$3)`,
		tenantID, conversation.ID, first.RunID, userID,
	).Scan(&conversations, &runs, &messages, &events, &outbox, &jobs, &responses, &contextSnapshots, &contextEvents, &contextOutbox, &contextID, &contextRef, &contextHash, &contextFocusVersion, &contextPreferencesVersion, &contextRouteID, &snapshotID, &profile, &environment, &channelID, &sequence); err != nil {
		t.Fatal(err)
	}
	if conversations != 1 || runs != 1 || messages != 2 || events != 5 || outbox != 6 || jobs != 1 || responses != 3 || contextSnapshots != 1 || contextEvents != 1 || contextOutbox != 1 || contextID == "" || contextFocusVersion != 3 || contextPreferencesVersion != 4 || contextRouteID != routeID || snapshotID != binding.SnapshotID || profile != string(behavior.Coach) || environment != "production" || channelID != binding.ChannelID || sequence != binding.Sequence {
		t.Fatalf("conversation=%d runs=%d messages=%d events=%d outbox=%d jobs=%d responses=%d context=%d/%d/%d binding=%s/%s/%s/%s/%d", conversations, runs, messages, events, outbox, jobs, responses, contextSnapshots, contextEvents, contextOutbox, snapshotID, profile, environment, channelID, sequence)
	}
	contextJSON, err := payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: contextID, Class: "run-message", ContentType: "application/json"}, payload.Manifest{Ref: contextRef, Hash: contextHash})
	var contextDocument struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err != nil || json.Unmarshal(contextJSON, &contextDocument) != nil || len(contextDocument.Content) != 1 || !strings.Contains(contextDocument.Content[0].Text, goalText) || !strings.Contains(contextDocument.Content[0].Text, routeText) || !strings.Contains(contextDocument.Content[0].Text, evidenceText) || !strings.Contains(contextDocument.Content[0].Text, "evidence:"+evidenceID) || !strings.Contains(contextDocument.Content[0].Text, `"locale":"zh-CN"`) {
		t.Fatalf("Coach context content invalid: %s error=%v", contextJSON, err)
	}
	blobs.mu.RLock()
	defer blobs.mu.RUnlock()
	for ref, encoded := range blobs.values {
		if bytes.Contains(encoded, []byte(secretContent)) || bytes.Contains(encoded, []byte(goalText)) || bytes.Contains(encoded, []byte(routeText)) || bytes.Contains(encoded, []byte(evidenceText)) {
			t.Fatalf("plaintext Message leaked to object %s", ref)
		}
	}
}
