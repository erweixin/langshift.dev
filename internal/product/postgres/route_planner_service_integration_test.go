//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/behavior"
	behaviorpostgres "github.com/langshift/lites/internal/behavior/postgres"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/integrationfixture"
	"github.com/langshift/lites/internal/payload"
	productapi "github.com/langshift/lites/internal/product/api"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestRoutePlannerDispatcherQueuesOnePinnedRunForDraftMission(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 22, 0, 0, 0, time.UTC)
	userID := "a8000000-0000-4000-8000-000000000001"
	tenantID := "a8000000-0000-4000-8000-000000000002"
	roleID := "a8000000-0000-4000-8000-000000000003"
	epoch := "a8000000-0000-4000-8000-000000000005"
	_, _ = admin.Exec(ctx, `DELETE FROM identity.tenants WHERE id=$1`, tenantID)
	_, _ = admin.Exec(ctx, `DELETE FROM identity.users WHERE id=$1`, userID)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'route-planner-owner-a8@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Route Planner Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'route-planner-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	binding, err := integrationfixture.SeedBehaviorChannel(ctx, admin, tenantID, userID, behavior.RoutePlanner, "production", now)
	if err != nil {
		t.Fatal(err)
	}
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	key := bytes.Repeat([]byte{0xa8}, 32)
	appender := eventAppender(now)
	routes := RouteStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	routeService := RouteService{Pool: pool, Store: routes, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xa9}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xaa}, 32), CursorKey: bytes.Repeat([]byte{0xab}, 32), IdempotencyTTL: 24 * time.Hour, BehaviorProfile: string(behavior.RoutePlanner), BehaviorEnvironment: "production", OntologySnapshotID: "ontology:1.0.0:" + strings.Repeat("b", 64), ContentSnapshotID: "content:1.0.0:" + strings.Repeat("c", 64), Now: func() time.Time { return now }}
	inbox := eventpostgres.InboxStore{Pool: pool, Epochs: artifactEpochStub{epoch}, Tokens: opaque.Manager{Purpose: "route-planner-inbox", Pepper: bytes.Repeat([]byte{0xac}, 32)}, LeaseTTL: 5 * time.Minute, Now: func() time.Time { return now }}
	missionStore := MissionFocusStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	missionService := MissionMutationService{Pool: pool, Store: missionStore, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xa9}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xaa}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	created, err := missionService.Create(ctx, productapi.CreateMissionCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "a8000000-0000-4000-8000-000000000006", ClientRequestID: "route-planner-mission-create-a8", IdempotencyKey: "route-planner-mission-create-key-a8", TenantID: tenantID, UserID: userID, SessionID: "a8000000-0000-4000-8000-000000000007"}, TargetRoleProfileID: roleID, Goal: "Build a production cloud agent"})
	if err != nil {
		t.Fatal(err)
	}
	missionID := created.Mission.ID
	var generateCommand eventpostgres.DeliveredCommand
	err = admin.QueryRow(ctx, `SELECT tenant_id::text,store_epoch::text,command_id::text,command_type,aggregate_kind,aggregate_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND command_type='GenerateMissionRoute' AND aggregate_id=$2`, tenantID, missionID).Scan(&generateCommand.TenantID, &generateCommand.StoreEpoch, &generateCommand.CommandID, &generateCommand.CommandType, &generateCommand.AggregateKind, &generateCommand.AggregateID, &generateCommand.PayloadRef, &generateCommand.PayloadHash)
	if err != nil {
		t.Fatal(err)
	}
	missionDispatcher := MissionRouteDispatcher{Routes: routeService, Inbox: inbox, ConsumerName: "route-planner-test"}
	generated, err := missionDispatcher.Dispatch(ctx, generateCommand)
	if err != nil || !generated.Claimed || !generated.Completed || generated.Replayed || generated.Route.RevisionID == "" || generated.Route.PlannerCommandID == "" {
		t.Fatalf("initial dispatch=%#v err=%v", generated, err)
	}
	generatedReplay, err := missionDispatcher.Dispatch(ctx, generateCommand)
	if err != nil || !generatedReplay.Completed || !generatedReplay.Replayed {
		t.Fatalf("initial replay=%#v err=%v", generatedReplay, err)
	}
	obsoleteMission, err := missionService.Create(ctx, productapi.CreateMissionCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "a8000000-0000-4000-8000-000000000031", ClientRequestID: "route-planner-obsolete-create-a8", IdempotencyKey: "route-planner-obsolete-key-a8", TenantID: tenantID, UserID: userID, SessionID: "a8000000-0000-4000-8000-000000000032"}, TargetRoleProfileID: roleID, Goal: "Verify delayed command suppression"})
	if err != nil {
		t.Fatal(err)
	}
	var obsoleteCommand eventpostgres.DeliveredCommand
	if err = admin.QueryRow(ctx, `SELECT tenant_id::text,store_epoch::text,command_id::text,command_type,aggregate_kind,aggregate_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND command_type='GenerateMissionRoute' AND aggregate_id=$2`, tenantID, obsoleteMission.Mission.ID).Scan(&obsoleteCommand.TenantID, &obsoleteCommand.StoreEpoch, &obsoleteCommand.CommandID, &obsoleteCommand.CommandType, &obsoleteCommand.AggregateKind, &obsoleteCommand.AggregateID, &obsoleteCommand.PayloadRef, &obsoleteCommand.PayloadHash); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE product.missions SET version=version+1,route_version=1 WHERE tenant_id=$1 AND id=$2`, tenantID, obsoleteMission.Mission.ID); err != nil {
		t.Fatal(err)
	}
	superseded, err := missionDispatcher.Dispatch(ctx, obsoleteCommand)
	var obsoleteRevisions, obsoleteInbox int
	queryErr := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.route_revisions WHERE tenant_id=$1 AND mission_id=$2),(SELECT count(*) FROM agent.inbox WHERE tenant_id=$1 AND command_id=$3 AND consumer_name='route-planner-test' AND status='completed')`, tenantID, obsoleteMission.Mission.ID, obsoleteCommand.CommandID).Scan(&obsoleteRevisions, &obsoleteInbox)
	if err != nil || queryErr != nil || !superseded.Claimed || !superseded.Completed || !superseded.Superseded || obsoleteRevisions != 0 || obsoleteInbox != 1 {
		t.Fatalf("obsolete=%#v revisions=%d inbox=%d err=%v query_err=%v", superseded, obsoleteRevisions, obsoleteInbox, err, queryErr)
	}
	var delivered eventpostgres.DeliveredCommand
	err = admin.QueryRow(ctx, `SELECT tenant_id::text,store_epoch::text,command_id::text,command_type,aggregate_kind,aggregate_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND command_type='RoutePlanningRequested' AND command_id=$2`, tenantID, generated.Route.PlannerCommandID).Scan(&delivered.TenantID, &delivered.StoreEpoch, &delivered.CommandID, &delivered.CommandType, &delivered.AggregateKind, &delivered.AggregateID, &delivered.PayloadRef, &delivered.PayloadHash)
	if err != nil {
		t.Fatal(err)
	}
	runs := executionpostgres.RunStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Behavior: behaviorpostgres.Store{}, Now: func() time.Time { return now }}
	dispatcher := RoutePlannerDispatcher{Service: RoutePlannerService{Pool: pool, Routes: routes, Runs: runs, Payloads: payloads, IDKey: key, RunTimeout: 30 * time.Minute, RunMaxSteps: 24, RunMaxCostMicrounits: 200000, RunMaxAttempts: 5, QueuePriority: 100, Now: func() time.Time { return now }}, Inbox: inbox, ConsumerName: "route-planner-test"}
	dispatched, err := dispatcher.Dispatch(ctx, delivered)
	if err != nil || !dispatched.Claimed || !dispatched.Completed || dispatched.Replayed || dispatched.Start.RunID == "" || dispatched.Start.RevisionID != generated.Route.RevisionID {
		t.Fatalf("dispatch=%#v err=%v", dispatched, err)
	}
	replayed, err := dispatcher.Dispatch(ctx, delivered)
	if err != nil || !replayed.Completed || !replayed.Replayed {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	var routeRunID, runSnapshot, runChannel, runEnvironment, runStatus, startCommandID, commandRef, commandHash string
	var runSequence uint64
	var runCount, conversationCount, userMessageCount, startCommandCount, completedInbox int
	err = admin.QueryRow(ctx, `SELECT
		r.planner_run_id::text,ar.profile_snapshot_id,ar.behavior_channel_id::text,ar.behavior_channel_sequence,ar.behavior_environment,ar.status,
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=ar.id),
		(SELECT count(*) FROM agent.conversations WHERE tenant_id=$1 AND id=ar.conversation_id AND mission_id=$2),
		(SELECT count(*) FROM agent.run_messages WHERE tenant_id=$1 AND run_id=ar.id AND role='user' AND source_kind='conversation_user'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='StartAgentRun' AND aggregate_id=ar.id),
		(SELECT count(*) FROM agent.inbox WHERE tenant_id=$1 AND command_id=$3 AND consumer_name='route-planner-test' AND status='completed'),
		o.command_id::text,o.payload_ref,o.payload_hash
		FROM product.route_revisions r
		JOIN agent.runs ar ON ar.tenant_id=r.tenant_id AND ar.id=r.planner_run_id
		JOIN agent.outbox o ON o.tenant_id=ar.tenant_id AND o.aggregate_id=ar.id AND o.command_type='StartAgentRun'
		WHERE r.tenant_id=$1 AND r.id=$4`, tenantID, missionID, delivered.CommandID, generated.Route.RevisionID).Scan(&routeRunID, &runSnapshot, &runChannel, &runSequence, &runEnvironment, &runStatus, &runCount, &conversationCount, &userMessageCount, &startCommandCount, &completedInbox, &startCommandID, &commandRef, &commandHash)
	if err != nil || routeRunID != dispatched.Start.RunID || runSnapshot != binding.SnapshotID || runChannel != binding.ChannelID || runSequence != binding.Sequence || runEnvironment != "production" || runStatus != "queued" || runCount != 1 || conversationCount != 1 || userMessageCount != 1 || startCommandCount != 1 || completedInbox != 1 {
		t.Fatalf("run=%s snapshot=%s channel=%s sequence=%d env=%s status=%s counts=%d/%d/%d/%d/%d err=%v", routeRunID, runSnapshot, runChannel, runSequence, runEnvironment, runStatus, runCount, conversationCount, userMessageCount, startCommandCount, completedInbox, err)
	}
	commandBody, err := payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: "unused", Class: "agent-run-command", ContentType: "application/json"}, payload.Manifest{Ref: commandRef, Hash: commandHash})
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		CorrelationID string `json:"correlation_id"`
	}
	if json.Unmarshal(commandBody, &start) != nil || start.SchemaVersion != 1 || start.RunID != routeRunID || start.CorrelationID == "" {
		t.Fatalf("invalid start command: %s", commandBody)
	}
	workerRuns := executionpostgres.RunStore{Pool: admin, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Behavior: behaviorpostgres.Store{}, Tokens: opaque.Manager{Purpose: "route-planner-run-lease", Pepper: bytes.Repeat([]byte{0xcc}, 32)}, LeaseTTL: 5 * time.Minute, Now: func() time.Time { return now }}
	claim, err := workerRuns.ClaimStart(ctx, executionpostgres.ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: startCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: routeRunID, PayloadRef: commandRef, PayloadHash: commandHash}, ConsumerName: "route-planner-agent-test", WorkerID: "route-planner-agent-worker", CorrelationID: start.CorrelationID, Actor: json.RawMessage(`{"kind":"service"}`), RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://route-planner/run-started", Hash: strings.Repeat("1", 64)}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://route-planner/attempt-started", Hash: strings.Repeat("2", 64)}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://route-planner/attempt-expired", Hash: strings.Repeat("3", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	routeJSON := []byte(`{"schema_version":1,"summary":"A grounded transition.","transferable_experience":[],"gaps":[{"statement":"Practice production delivery.","capability_ids":["capability-1"],"evidence_ids":[],"confidence":"inferred"}],"bridge":[{"id":"bridge_1","title":"Ship safely","rationale":"Turns existing analysis into delivery evidence.","from_capability_ids":[],"to_capability_ids":["capability-1"]}],"stages":[{"id":"stage_1","title":"Foundation","outcome":"A reviewed plan.","capability_ids":["capability-1"],"evidence_required":["reviewed plan"]},{"id":"stage_2","title":"Delivery","outcome":"A deployed artifact.","capability_ids":["capability-1"],"evidence_required":["deployment record"]}],"first_task":{"title":"Draft the plan","objective":"Produce a reviewable delivery plan.","estimated_minutes":45,"difficulty":"standard","capability_ids":["capability-1"],"success_criteria":["Plan is reviewed"]}}`)
	messageJSON, err := json.Marshal(map[string]any{"schema_version": 1, "role": "assistant", "content": []map[string]any{{"type": "text", "text": string(routeJSON)}}})
	if err != nil {
		t.Fatal(err)
	}
	messageID := "a8000000-0000-4000-8000-000000000020"
	messageManifest, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: messageID, Class: "run-message", ContentType: "application/json"}, messageJSON)
	if err != nil {
		t.Fatal(err)
	}
	messageDigest := sha256.Sum256(messageJSON)
	completed, err := workerRuns.CompleteRunWithMessage(ctx, executionpostgres.CompleteRunMessageCommand{Completion: executionpostgres.CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: "succeeded", ResultHash: strings.Repeat("4", 64), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: start.CorrelationID, RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://route-planner/run-succeeded", Hash: strings.Repeat("5", 64)}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://route-planner/attempt-completed", Hash: strings.Repeat("6", 64)}}, MessageID: messageID, Message: executionpostgres.PayloadPointer{Ref: messageManifest.Ref, Hash: messageManifest.Hash}, ContentHash: hex.EncodeToString(messageDigest[:]), FinalizedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://route-planner/message-finalized", Hash: strings.Repeat("8", 64)}})
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	reconciler := RoutePlannerReconciler{Pool: pool, Routes: routes, Payloads: payloads, IDKey: key, Now: func() time.Time { return now }}
	tenants, err := reconciler.ListTenantIDs(ctx, "", 100)
	if err != nil || len(tenants) == 0 {
		t.Fatalf("reconciliation tenants=%v err=%v", tenants, err)
	}
	reconciled, err := reconciler.ReconcileTenant(ctx, tenantID, 100)
	if err != nil || reconciled.Scanned != 1 || reconciled.Proposed != 1 || reconciled.Failed != 0 {
		t.Fatalf("reconciled=%#v err=%v", reconciled, err)
	}
	listed, err := routeService.List(ctx, productapi.RouteListQuery{TenantID: tenantID, UserID: userID, MissionID: missionID})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Status != "proposed" || len(listed.Items[0].Route) == 0 {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
}
