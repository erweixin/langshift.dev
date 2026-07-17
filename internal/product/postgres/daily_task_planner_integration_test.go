//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
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
	productreminder "github.com/langshift/lites/internal/product/reminder"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestDailyTaskPlannerQueuesPinnedRunAndSchedulesExactlyOneTask(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 23, 0, 0, 0, time.UTC)
	userID := "b8000000-0000-4000-8000-000000000001"
	tenantID := "b8000000-0000-4000-8000-000000000002"
	roleID := "b8000000-0000-4000-8000-000000000003"
	missionID := "b8000000-0000-4000-8000-000000000004"
	routeID := "b8000000-0000-4000-8000-000000000005"
	commandID := "b8000000-0000-4000-8000-000000000006"
	epoch := "b8000000-0000-4000-8000-000000000007"
	_, _ = admin.Exec(ctx, `DELETE FROM identity.tenants WHERE id=$1`, tenantID)
	_, _ = admin.Exec(ctx, `DELETE FROM identity.users WHERE id=$1`, userID)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'daily-planner-owner-b8@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Daily Planner Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'daily-planner-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,$5)`, []any{missionID, tenantID, userID, roleID, strings.Repeat("8", 64)}},
		{`INSERT INTO product.mission_focuses(tenant_id,user_id,mission_id,focus_version) VALUES($1,$2,$3,3)`, []any{tenantID, userID, missionID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	binding, err := integrationfixture.SeedBehaviorChannel(ctx, admin, tenantID, userID, behavior.DailyPlanner, "production", now)
	if err != nil {
		t.Fatal(err)
	}
	evaluatorBinding, err := integrationfixture.SeedBehaviorChannel(ctx, admin, tenantID, userID, behavior.Evaluator, "production", now)
	if err != nil {
		t.Fatal(err)
	}
	rubricID := "b8000000-0000-4000-8000-000000000050"
	if _, err = admin.Exec(ctx, `INSERT INTO product.rubric_versions(id,tenant_id,slug,revision,status,spec,dimensions,scoring_rules) VALUES($1,$2,'production-delivery',1,'active','{"purpose":"review production delivery"}','[{"id":"correctness"}]','{"pass_score":70}')`, rubricID, tenantID); err != nil {
		t.Fatal(err)
	}
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	routeJSON := []byte(`{"schema_version":1,"summary":"A grounded transition.","transferable_experience":[],"gaps":[{"statement":"Practice production delivery.","capability_ids":["capability-1"],"evidence_ids":[],"confidence":"inferred"}],"bridge":[{"id":"bridge_1","title":"Ship safely","rationale":"Turns analysis into delivery evidence.","from_capability_ids":[],"to_capability_ids":["capability-1"]}],"stages":[{"id":"stage_1","title":"Foundation","outcome":"A reviewed plan.","capability_ids":["capability-1"],"evidence_required":["reviewed plan"]},{"id":"stage_2","title":"Delivery","outcome":"A deployed artifact.","capability_ids":["capability-1"],"evidence_required":["deployment record"]}],"first_task":{"title":"Draft the plan","objective":"Produce a reviewable delivery plan.","estimated_minutes":45,"difficulty":"standard","capability_ids":["capability-1"],"success_criteria":["Plan is reviewed"]}}`)
	routePayload, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: routeID, Class: routePayloadClass, ContentType: "application/json"}, routeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,route_payload_hash,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted',$5,0,'{}',$6,$7,'route@1','ontology@1','content@1',$8)`, routeID, tenantID, userID, missionID, strings.Repeat("8", 64), routePayload.Ref, routePayload.Hash, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE product.missions SET current_route_revision_id=$1 WHERE tenant_id=$2 AND id=$3`, routeID, tenantID, missionID); err != nil {
		t.Fatal(err)
	}
	commandDocument := dailyTaskCommandDocument{SchemaVersion: 1, MissionID: missionID, UserID: userID, RouteRevisionID: routeID, ExpectedRouteVersion: 1, ExpectedFocusVersion: 3, ScheduledFor: "2026-07-16", Difficulty: "standard", AvailableMinutes: 45, CorrelationID: "b8000000-0000-4000-8000-000000000008"}
	commandPayload, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: commandID, Class: missionCommandClass, ContentType: "application/json"}, mustJSON(t, commandDocument))
	if err != nil {
		t.Fatal(err)
	}
	delivered := eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: commandID, CommandType: "GenerateDailyTask", AggregateKind: "mission", AggregateID: missionID, PayloadRef: commandPayload.Ref, PayloadHash: commandPayload.Hash}
	key := bytes.Repeat([]byte{0xb8}, 32)
	appender := eventAppender(now)
	routes := RouteStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	runs := executionpostgres.RunStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Behavior: behaviorpostgres.Store{}, Now: func() time.Time { return now }}
	inbox := eventpostgres.InboxStore{Pool: pool, Epochs: artifactEpochStub{epoch}, Tokens: opaque.Manager{Purpose: "daily-planner-inbox", Pepper: bytes.Repeat([]byte{0xb9}, 32)}, LeaseTTL: 5 * time.Minute, Now: func() time.Time { return now }}
	dispatcher := DailyTaskPlannerDispatcher{Service: DailyTaskPlannerService{Pool: pool, Routes: routes, Runs: runs, Payloads: payloads, IDKey: key, BehaviorEnvironment: "production", ContentSnapshotID: "content:1.0.0:" + strings.Repeat("c", 64), RunTimeout: 30 * time.Minute, RunMaxSteps: 24, RunMaxCostMicrounits: 200000, RunMaxAttempts: 5, QueuePriority: 100, Now: func() time.Time { return now }}, Inbox: inbox, ConsumerName: "daily-planner-test"}
	dispatched, err := dispatcher.Dispatch(ctx, delivered)
	if err != nil || !dispatched.Completed || dispatched.Superseded || dispatched.RunID == "" {
		t.Fatalf("dispatch=%#v err=%v", dispatched, err)
	}
	replayed, err := dispatcher.Dispatch(ctx, delivered)
	if err != nil || !replayed.Replayed || !replayed.Completed {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	var startCommandID, commandRef, commandHash string
	if err = admin.QueryRow(ctx, `SELECT o.command_id::text,o.payload_ref,o.payload_hash FROM product.daily_task_generations g JOIN agent.outbox o ON o.tenant_id=g.tenant_id AND o.aggregate_id=g.planner_run_id AND o.command_type='StartAgentRun' WHERE g.tenant_id=$1 AND g.id=$2 AND g.behavior_snapshot_id=$3 AND g.behavior_channel_id=$4`, tenantID, dispatched.GenerationID, binding.SnapshotID, binding.ChannelID).Scan(&startCommandID, &commandRef, &commandHash); err != nil {
		t.Fatal(err)
	}
	startBody, err := payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: startCommandID, Class: "agent-run-command", ContentType: "application/json"}, payload.Manifest{Ref: commandRef, Hash: commandHash})
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		CorrelationID string `json:"correlation_id"`
	}
	if json.Unmarshal(startBody, &start) != nil || start.SchemaVersion != 1 || start.RunID != dispatched.RunID {
		t.Fatalf("start=%s", startBody)
	}
	workerRuns := executionpostgres.RunStore{Pool: admin, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Behavior: behaviorpostgres.Store{}, Tokens: opaque.Manager{Purpose: "daily-planner-run", Pepper: bytes.Repeat([]byte{0xba}, 32)}, LeaseTTL: 5 * time.Minute, Now: func() time.Time { return now }}
	claim, err := workerRuns.ClaimStart(ctx, executionpostgres.ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: startCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: dispatched.RunID, PayloadRef: commandRef, PayloadHash: commandHash}, ConsumerName: "daily-agent-test", WorkerID: "daily-agent-worker", CorrelationID: start.CorrelationID, Actor: json.RawMessage(`{"kind":"service"}`), RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://daily/run-started", Hash: strings.Repeat("1", 64)}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://daily/attempt-started", Hash: strings.Repeat("2", 64)}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://daily/attempt-expired", Hash: strings.Repeat("3", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	taskJSON := []byte(`{"schema_version":1,"title":"Ship one safe change","objective":"Produce a reviewable production change.","why_this_task":"It closes the first route capability gap.","key_judgment":"Choose the smallest safe boundary.","estimated_minutes":45,"difficulty":"standard","practice_kind":"code","explanation":[{"title":"Define the boundary","content":"State the invariant before editing."}],"example":"A transaction commits state and event together.","practice":{"instructions":"Implement and verify one transactional change.","starter_content":"","deterministic_checks":["Targeted tests pass"],"success_criteria":["The invariant is enforced"]},"capability_ids":["capability-1"],"evidence_targets":["test report"],"next_task_hint":"Exercise the rollback path."}`)
	messageJSON := mustJSON(t, map[string]any{"schema_version": 1, "role": "assistant", "content": []map[string]any{{"type": "text", "text": string(taskJSON)}}})
	messageID := "b8000000-0000-4000-8000-000000000020"
	messagePayload, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: messageID, Class: "run-message", ContentType: "application/json"}, messageJSON)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(messageJSON)
	if _, err = workerRuns.CompleteRunWithMessage(ctx, executionpostgres.CompleteRunMessageCommand{Completion: executionpostgres.CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: "succeeded", ResultHash: strings.Repeat("4", 64), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: start.CorrelationID, RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://daily/run-succeeded", Hash: strings.Repeat("5", 64)}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://daily/attempt-completed", Hash: strings.Repeat("6", 64)}}, MessageID: messageID, Message: executionpostgres.PayloadPointer{Ref: messagePayload.Ref, Hash: messagePayload.Hash}, ContentHash: hex.EncodeToString(digest[:]), FinalizedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://daily/message-finalized", Hash: strings.Repeat("7", 64)}}); err != nil {
		t.Fatal(err)
	}
	reconciler := DailyTaskPlannerReconciler{Pool: pool, Routes: routes, Payloads: payloads, IDKey: key, Now: func() time.Time { return now }}
	tenants, err := reconciler.ListTenantIDs(ctx, "", 100)
	if err != nil || len(tenants) == 0 {
		t.Fatalf("tenants=%v err=%v", tenants, err)
	}
	result, err := reconciler.ReconcileTenant(ctx, tenantID, 100)
	if err != nil || result.Scheduled != 1 || result.Failed != 0 || result.Superseded != 0 {
		t.Fatalf("reconcile=%#v err=%v", result, err)
	}
	result, err = reconciler.ReconcileTenant(ctx, tenantID, 100)
	var tasks, events int
	queryErr := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.daily_tasks WHERE tenant_id=$1 AND user_id=$2 AND scheduled_for='2026-07-16'),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type='DailyTaskScheduled')`, tenantID, userID).Scan(&tasks, &events)
	if err != nil || result.Scanned != 0 || queryErr != nil || tasks != 1 || events != 1 {
		t.Fatalf("replay=%#v tasks=%d events=%d err=%v query=%v", result, tasks, events, err, queryErr)
	}
	taskService := DailyTaskService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: key, CursorKey: bytes.Repeat([]byte{0xbb}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xbc}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xbd}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	listed, err := taskService.List(ctx, productapi.DailyTaskListQuery{TenantID: tenantID, UserID: userID})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Status != "scheduled" || !bytes.Equal(listed.Items[0].Task, taskJSON) {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	taskID := listed.Items[0].ID
	startTask := productapi.UpdateDailyTaskCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "b8000000-0000-4000-8000-000000000040", ClientRequestID: "daily-task-start-b8", IdempotencyKey: "daily-task-start-key-b8", TenantID: tenantID, UserID: userID, SessionID: "b8000000-0000-4000-8000-000000000041"}, TaskID: taskID, Action: "start", ExpectedTaskVersion: 1}
	started, err := taskService.Update(ctx, startTask)
	if err != nil || started.Version != 2 || started.Status != "in_progress" || started.Replayed {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	startedReplay, err := taskService.Update(ctx, startTask)
	if err != nil || !startedReplay.Replayed || startedReplay.Version != 2 {
		t.Fatalf("start replay=%#v err=%v", startedReplay, err)
	}
	submissionService := SubmissionService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xbe}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xbf}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	submissionCommand := productapi.CreateSubmissionCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "b8000000-0000-4000-8000-000000000042", ClientRequestID: "submission-create-b8", IdempotencyKey: "submission-create-key-b8", TenantID: tenantID, UserID: userID, SessionID: "b8000000-0000-4000-8000-000000000041"}, DailyTaskID: taskID, ExpectedTaskVersion: 2, SubmissionKind: "code", Content: "package main\n// verified submission", Understanding: "The transaction binds state and event."}
	submitted, err := submissionService.Create(ctx, submissionCommand)
	if err != nil || submitted.DailyTaskVersion != 3 || submitted.Status != "submitted" {
		t.Fatalf("submitted=%#v err=%v", submitted, err)
	}
	submissionReplay, err := submissionService.Create(ctx, submissionCommand)
	if err != nil || !submissionReplay.Replayed {
		t.Fatalf("submission replay=%#v err=%v", submissionReplay, err)
	}
	reviewService := ReviewService{Pool: pool, Runs: runs, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xc0}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xc1}, 32), BehaviorEnvironment: "production", RunTimeout: 30 * time.Minute, RunMaxSteps: 8, RunMaxCostMicrounits: 200000, RunMaxAttempts: 3, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	reviewCommand := productapi.GenerateReviewCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "b8000000-0000-4000-8000-000000000043", ClientRequestID: "review-generate-b8", IdempotencyKey: "review-generate-key-b8", TenantID: tenantID, UserID: userID, SessionID: "b8000000-0000-4000-8000-000000000041"}, SubmissionID: submitted.ID, RubricVersionID: rubricID, ExpectedSubmissionRevision: 1, ExpectedTaskVersion: 3}
	generated, err := reviewService.Generate(ctx, reviewCommand)
	if err != nil || generated.RunID == "" || generated.Status != "queued" {
		t.Fatalf("generated=%#v err=%v", generated, err)
	}
	var reviewStartID, reviewStartRef, reviewStartHash string
	if err = admin.QueryRow(ctx, `SELECT o.command_id::text,o.payload_ref,o.payload_hash FROM product.submission_review_generations g JOIN agent.outbox o ON o.tenant_id=g.tenant_id AND o.aggregate_id=g.review_run_id AND o.command_type='StartAgentRun' WHERE g.tenant_id=$1 AND g.id=$2 AND g.behavior_snapshot_id=$3 AND g.behavior_channel_id=$4`, tenantID, generated.GenerationID, evaluatorBinding.SnapshotID, evaluatorBinding.ChannelID).Scan(&reviewStartID, &reviewStartRef, &reviewStartHash); err != nil {
		t.Fatal(err)
	}
	reviewStartBody, err := payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: reviewStartID, Class: "agent-run-command", ContentType: "application/json"}, payload.Manifest{Ref: reviewStartRef, Hash: reviewStartHash})
	if err != nil {
		t.Fatal(err)
	}
	var reviewStart struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		CorrelationID string `json:"correlation_id"`
	}
	if json.Unmarshal(reviewStartBody, &reviewStart) != nil || reviewStart.RunID != generated.RunID {
		t.Fatalf("review start=%s", reviewStartBody)
	}
	reviewClaim, err := workerRuns.ClaimStart(ctx, executionpostgres.ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: reviewStartID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: generated.RunID, PayloadRef: reviewStartRef, PayloadHash: reviewStartHash}, ConsumerName: "evaluator-agent-test", WorkerID: "evaluator-worker", CorrelationID: reviewStart.CorrelationID, Actor: json.RawMessage(`{"kind":"service"}`), RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://review/run-started", Hash: strings.Repeat("1", 64)}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://review/attempt-started", Hash: strings.Repeat("2", 64)}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://review/attempt-expired", Hash: strings.Repeat("3", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	reviewJSON := []byte(`{"schema_version":1,"verdict":"pass","summary":"The invariant is explained and implemented.","deterministic_results":{"checks":[{"name":"submission present","status":"pass","evidence":"The submitted source is non-empty."}]},"dimensions":[{"id":"correctness","score":90,"rationale":"The transaction boundary is explicit.","evidence_quotes":["binds state and event"]}],"strengths":["Clear invariant"],"improvements":[],"capability_evidence":[{"capability_id":"capability-1","level":"demonstrated","statement":"The submitted artifact demonstrates the route capability."}],"next_action":"Exercise rollback.","uncertainty":"The evaluator did not execute external infrastructure."}`)
	reviewMessageJSON := mustJSON(t, map[string]any{"schema_version": 1, "role": "assistant", "content": []map[string]any{{"type": "text", "text": string(reviewJSON)}}})
	reviewMessageID := "b8000000-0000-4000-8000-000000000051"
	reviewMessagePayload, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: reviewMessageID, Class: "run-message", ContentType: "application/json"}, reviewMessageJSON)
	if err != nil {
		t.Fatal(err)
	}
	reviewDigest := sha256.Sum256(reviewMessageJSON)
	if _, err = workerRuns.CompleteRunWithMessage(ctx, executionpostgres.CompleteRunMessageCommand{Completion: executionpostgres.CompleteRunCommand{Claim: reviewClaim, ExpectedRunVersion: reviewClaim.RunVersion, TargetState: "succeeded", ResultHash: strings.Repeat("4", 64), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: reviewStart.CorrelationID, RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://review/run-succeeded", Hash: strings.Repeat("5", 64)}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://review/attempt-completed", Hash: strings.Repeat("6", 64)}}, MessageID: reviewMessageID, Message: executionpostgres.PayloadPointer{Ref: reviewMessagePayload.Ref, Hash: reviewMessagePayload.Hash}, ContentHash: hex.EncodeToString(reviewDigest[:]), FinalizedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://review/message-finalized", Hash: strings.Repeat("7", 64)}}); err != nil {
		t.Fatal(err)
	}
	reviewReconciler := ReviewReconciler{Pool: pool, Appender: appender, Payloads: payloads, IDKey: key, StoreEpoch: epoch, Now: func() time.Time { return now }}
	reviewResult, err := reviewReconciler.ReconcileTenant(ctx, tenantID, 100)
	if err != nil || reviewResult.Succeeded != 1 {
		t.Fatalf("review reconcile=%#v err=%v", reviewResult, err)
	}
	var reviewCount, evidenceCount int
	var taskStatus string
	var finalTaskVersion uint64
	queryErr = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.reviews WHERE tenant_id=$1 AND submission_id=$2),(SELECT count(*) FROM product.evidence WHERE tenant_id=$1 AND source_kind='review' AND source_id IN (SELECT id FROM product.reviews WHERE tenant_id=$1)),status,version FROM product.daily_tasks WHERE tenant_id=$1 AND id=$3`, tenantID, submitted.ID, taskID).Scan(&reviewCount, &evidenceCount, &taskStatus, &finalTaskVersion)
	if queryErr != nil || reviewCount != 1 || evidenceCount != 1 || taskStatus != "completed" || finalTaskVersion != 5 {
		t.Fatalf("review=%d evidence=%d task=%s/%d query=%v", reviewCount, evidenceCount, taskStatus, finalTaskVersion, queryErr)
	}
	var reviewID string
	if err = admin.QueryRow(ctx, `SELECT id::text FROM product.reviews WHERE tenant_id=$1 AND submission_id=$2`, tenantID, submitted.ID).Scan(&reviewID); err != nil {
		t.Fatal(err)
	}
	reviewRead, err := reviewService.Get(ctx, productapi.ReviewGetQuery{TenantID: tenantID, UserID: userID, ReviewID: reviewID})
	if err != nil || reviewRead.EvidenceID == "" || !bytes.Equal(reviewRead.Review, reviewJSON) {
		t.Fatalf("review read=%#v err=%v", reviewRead, err)
	}
	evidenceService := EvidenceService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: key, CursorKey: bytes.Repeat([]byte{0xc2}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xc3}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xc4}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	manualContent := "A production test report with an immutable checksum."
	manualDigest := sha256.Sum256([]byte(manualContent))
	manualCommand := productapi.RecordEvidenceCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "b8000000-0000-4000-8000-000000000052", ClientRequestID: "evidence-record-b8", IdempotencyKey: "evidence-record-key-b8", TenantID: tenantID, UserID: userID, SessionID: "b8000000-0000-4000-8000-000000000041"}, MissionID: missionID, EvidenceType: "test_report", SourceKind: "test_result", Content: manualContent, ContentHash: hex.EncodeToString(manualDigest[:])}
	manualEvidence, err := evidenceService.Record(ctx, manualCommand)
	if err != nil || manualEvidence.Status != "recorded" {
		t.Fatalf("manual evidence=%#v err=%v", manualEvidence, err)
	}
	manualReplay, err := evidenceService.Record(ctx, manualCommand)
	if err != nil || !manualReplay.Replayed {
		t.Fatalf("manual replay=%#v err=%v", manualReplay, err)
	}
	evidenceList, err := evidenceService.List(ctx, productapi.EvidenceListQuery{TenantID: tenantID, UserID: userID})
	if err != nil || len(evidenceList.Items) != 2 {
		t.Fatalf("evidence list=%#v err=%v", evidenceList, err)
	}
	preferencesService := PreferencesService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xc5}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xc6}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	defaults, err := preferencesService.Get(ctx, productapi.PreferencesQuery{TenantID: tenantID, UserID: userID})
	if err != nil || defaults.Version != 1 || defaults.Timezone != "UTC" {
		t.Fatalf("defaults=%#v err=%v", defaults, err)
	}
	preferenceCommand := productapi.UpdatePreferencesCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "b8000000-0000-4000-8000-000000000053", ClientRequestID: "preferences-update-b8", IdempotencyKey: "preferences-update-key-b8", TenantID: tenantID, UserID: userID, SessionID: "b8000000-0000-4000-8000-000000000041"}, Locale: "zh-CN", Timezone: "Asia/Shanghai", CoachPreferences: productapi.CoachPreferences{SchemaVersion: 1, Difficulty: "harder", AvailableMinutes: 60, Tone: "socratic", ExplanationDepth: "deep"}, ExpectedVersion: 1}
	updatedPreferences, err := preferencesService.Update(ctx, preferenceCommand)
	if err != nil || updatedPreferences.Version != 2 || updatedPreferences.Timezone != "Asia/Shanghai" {
		t.Fatalf("updated preferences=%#v err=%v", updatedPreferences, err)
	}
	preferenceReplay, err := preferencesService.Update(ctx, preferenceCommand)
	if err != nil || !preferenceReplay.Replayed {
		t.Fatalf("preference replay=%#v err=%v", preferenceReplay, err)
	}
	storedPreferences, err := preferencesService.Get(ctx, productapi.PreferencesQuery{TenantID: tenantID, UserID: userID})
	if err != nil || storedPreferences.Version != 2 || storedPreferences.CoachPreferences.Difficulty != "harder" {
		t.Fatalf("stored preferences=%#v err=%v", storedPreferences, err)
	}
	reminderService := ReminderService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xc7}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xc8}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	createReminder := productapi.CreateReminderCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "b8000000-0000-4000-8000-000000000054", ClientRequestID: "reminder-create-b8", IdempotencyKey: "reminder-create-key-b8", TenantID: tenantID, UserID: userID, SessionID: "b8000000-0000-4000-8000-000000000041"}, Timezone: "America/New_York", LocalTime: "02:30", Weekdays: []int{7, 1, 3}, Channel: "push"}
	createdReminder, err := reminderService.Create(ctx, createReminder)
	if err != nil || createdReminder.Version != 1 || createdReminder.Status != "active" || createdReminder.NextOccurrenceAt == nil || !slices.Equal(createdReminder.Weekdays, []int{1, 3, 7}) {
		t.Fatalf("created reminder=%#v err=%v", createdReminder, err)
	}
	createdReplay, err := reminderService.Create(ctx, createReminder)
	if err != nil || !createdReplay.Replayed || createdReplay.ID != createdReminder.ID {
		t.Fatalf("reminder replay=%#v err=%v", createdReplay, err)
	}
	listedReminders, err := reminderService.List(ctx, productapi.ReminderListQuery{TenantID: tenantID, UserID: userID})
	if err != nil || len(listedReminders.Items) != 1 || listedReminders.Items[0].ID != createdReminder.ID {
		t.Fatalf("listed reminders=%#v err=%v", listedReminders, err)
	}
	foreignReminders, err := reminderService.List(ctx, productapi.ReminderListQuery{TenantID: "b8000000-0000-4000-8000-000000000099", UserID: userID})
	if err != nil || len(foreignReminders.Items) != 0 {
		t.Fatalf("foreign reminders=%#v err=%v", foreignReminders, err)
	}
	updateReminder := func(requestID, clientID, key, action string, expected uint64) productapi.ReminderResource {
		t.Helper()
		result, updateErr := reminderService.Update(ctx, productapi.UpdateReminderCommand{CommandMetadata: productapi.CommandMetadata{RequestID: requestID, ClientRequestID: clientID, IdempotencyKey: key, TenantID: tenantID, UserID: userID, SessionID: "b8000000-0000-4000-8000-000000000041"}, ReminderID: createdReminder.ID, Action: action, ExpectedVersion: expected})
		if updateErr != nil {
			t.Fatalf("%s reminder: %v", action, updateErr)
		}
		return result
	}
	pausedReminder := updateReminder("b8000000-0000-4000-8000-000000000055", "reminder-pause-b8", "reminder-pause-key-b8", "pause", 1)
	if pausedReminder.Version != 2 || pausedReminder.Status != "paused" || pausedReminder.NextOccurrenceAt != nil {
		t.Fatalf("paused reminder=%#v", pausedReminder)
	}
	resumedReminder := updateReminder("b8000000-0000-4000-8000-000000000056", "reminder-resume-b8", "reminder-resume-key-b8", "resume", 2)
	if resumedReminder.Version != 3 || resumedReminder.Status != "active" || resumedReminder.NextOccurrenceAt == nil {
		t.Fatalf("resumed reminder=%#v", resumedReminder)
	}
	cancelledReminder := updateReminder("b8000000-0000-4000-8000-000000000057", "reminder-cancel-b8", "reminder-cancel-key-b8", "cancel", 3)
	if cancelledReminder.Version != 4 || cancelledReminder.Status != "cancelled" || cancelledReminder.NextOccurrenceAt != nil || cancelledReminder.CancelledAt == nil {
		t.Fatalf("cancelled reminder=%#v", cancelledReminder)
	}
	_, err = reminderService.Update(ctx, productapi.UpdateReminderCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "b8000000-0000-4000-8000-000000000058", ClientRequestID: "reminder-terminal-b8", IdempotencyKey: "reminder-terminal-key-b8", TenantID: tenantID, UserID: userID, SessionID: "b8000000-0000-4000-8000-000000000041"}, ReminderID: createdReminder.ID, Action: "resume", ExpectedVersion: 4})
	if !errors.Is(err, productapi.ErrStateConflict) {
		t.Fatalf("terminal update error=%v", err)
	}
	var reminderEvents int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='reminder_schedule' AND aggregate_id=$2 AND aggregate_version BETWEEN 1 AND 4`, tenantID, createdReminder.ID).Scan(&reminderEvents); err != nil || reminderEvents != 4 {
		t.Fatalf("reminder events=%d err=%v", reminderEvents, err)
	}
	dueCommand := createReminder
	dueCommand.RequestID = "b8000000-0000-4000-8000-000000000059"
	dueCommand.ClientRequestID = "reminder-due-b8"
	dueCommand.IdempotencyKey = "reminder-due-key-b8"
	dueCommand.Timezone, dueCommand.LocalTime, dueCommand.Weekdays, dueCommand.Channel = "Asia/Shanghai", "08:00", []int{1, 2, 3, 4, 5, 6, 7}, "in_app"
	dueReminder, err := reminderService.Create(ctx, dueCommand)
	if err != nil || dueReminder.Version != 1 {
		t.Fatalf("due reminder=%#v err=%v", dueReminder, err)
	}
	forcedOccurrence := now.Add(-time.Minute)
	if _, err = admin.Exec(ctx, `UPDATE product.reminder_schedules SET next_occurrence_at=$1 WHERE tenant_id=$2 AND id=$3`, forcedOccurrence, tenantID, dueReminder.ID); err != nil {
		t.Fatal(err)
	}
	scheduler := ReminderScheduler{Pool: pool, Appender: appender, Payloads: payloads, IDKey: key, StoreEpoch: epoch, Now: func() time.Time { return now }}
	scheduled, err := scheduler.ScheduleTenant(ctx, tenantID, 100)
	if err != nil || scheduled.Scheduled != 1 || scheduled.Scanned != 1 {
		t.Fatalf("scheduled=%#v err=%v", scheduled, err)
	}
	repeatedSchedule, err := scheduler.ScheduleTenant(ctx, tenantID, 100)
	if err != nil || repeatedSchedule.Scheduled != 0 || repeatedSchedule.Scanned != 0 {
		t.Fatalf("repeated schedule=%#v err=%v", repeatedSchedule, err)
	}
	var deliveryID, deliveryKey, deliveryStatus, deliveryCommandID, deliveryCommandRef, deliveryCommandHash string
	var advancedOccurrence time.Time
	var advancedScheduleVersion uint64
	if err = admin.QueryRow(ctx, `SELECT d.id::text,d.delivery_key,d.status,o.command_id::text,o.payload_ref,o.payload_hash,s.version,s.next_occurrence_at FROM product.reminder_deliveries d JOIN product.reminder_schedules s ON s.tenant_id=d.tenant_id AND s.id=d.schedule_id JOIN agent.outbox o ON o.tenant_id=d.tenant_id AND o.aggregate_kind='reminder_delivery' AND o.aggregate_id=d.id AND o.command_type='DeliverReminder' WHERE d.tenant_id=$1 AND d.schedule_id=$2`, tenantID, dueReminder.ID).Scan(&deliveryID, &deliveryKey, &deliveryStatus, &deliveryCommandID, &deliveryCommandRef, &deliveryCommandHash, &advancedScheduleVersion, &advancedOccurrence); err != nil || deliveryStatus != "pending" || len(deliveryKey) != 64 || advancedScheduleVersion != 2 || !advancedOccurrence.After(forcedOccurrence) {
		t.Fatalf("delivery=%s key=%s status=%s schedule=%d/%s err=%v", deliveryID, deliveryKey, deliveryStatus, advancedScheduleVersion, advancedOccurrence, err)
	}
	deliveryCommandBody, err := payloads.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: deliveryCommandID, Class: reminderDeliveryCommandClass, ContentType: "application/json"}, payload.Manifest{Ref: deliveryCommandRef, Hash: deliveryCommandHash})
	if err != nil {
		t.Fatal(err)
	}
	var deliveryCommand ReminderDeliveryCommand
	if json.Unmarshal(deliveryCommandBody, &deliveryCommand) != nil || deliveryCommand.SchemaVersion != 1 || deliveryCommand.DeliveryID != deliveryID || deliveryCommand.DeliveryKey != deliveryKey || deliveryCommand.Locale != "zh-CN" || deliveryCommand.Channel != "in_app" {
		t.Fatalf("delivery command=%s", deliveryCommandBody)
	}
	reminderSender := &recordingReminderSender{}
	deliveryDispatcher := ReminderDeliveryDispatcher{Pool: pool, Appender: appender, Payloads: payloads, Inbox: inbox, Sender: reminderSender, IDKey: key, StoreEpoch: epoch, ConsumerName: "reminder-delivery-test", Now: func() time.Time { return now }}
	deliveredReminder, err := deliveryDispatcher.Dispatch(ctx, eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: deliveryCommandID, CommandType: "DeliverReminder", AggregateKind: "reminder_delivery", AggregateID: deliveryID, PayloadRef: deliveryCommandRef, PayloadHash: deliveryCommandHash})
	if err != nil || !deliveredReminder.Completed || deliveredReminder.Attempt != 1 || len(reminderSender.deliveries) != 1 || reminderSender.deliveries[0].DeliveryKey != deliveryKey {
		t.Fatalf("delivered reminder=%#v sends=%#v err=%v", deliveredReminder, reminderSender.deliveries, err)
	}
	replayedReminder, err := deliveryDispatcher.Dispatch(ctx, eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: deliveryCommandID, CommandType: "DeliverReminder", AggregateKind: "reminder_delivery", AggregateID: deliveryID, PayloadRef: deliveryCommandRef, PayloadHash: deliveryCommandHash})
	if err != nil || !replayedReminder.Completed || !replayedReminder.Replayed || len(reminderSender.deliveries) != 1 {
		t.Fatalf("replayed reminder=%#v sends=%d err=%v", replayedReminder, len(reminderSender.deliveries), err)
	}
	var deliveredStatus string
	var deliveredVersion uint64
	var deliveredAttempts, deliveryEvents int
	var deliveredAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT d.status,d.version,d.attempt_count,d.delivered_at,(SELECT count(*) FROM agent.events e WHERE e.tenant_id=d.tenant_id AND e.aggregate_kind='reminder_delivery' AND e.aggregate_id=d.id) FROM product.reminder_deliveries d WHERE d.tenant_id=$1 AND d.id=$2`, tenantID, deliveryID).Scan(&deliveredStatus, &deliveredVersion, &deliveredAttempts, &deliveredAt, &deliveryEvents); err != nil || deliveredStatus != "delivered" || deliveredVersion != 3 || deliveredAttempts != 1 || deliveredAt == nil || deliveryEvents != 3 {
		t.Fatalf("delivered state=%s/%d attempts=%d at=%v events=%d err=%v", deliveredStatus, deliveredVersion, deliveredAttempts, deliveredAt, deliveryEvents, err)
	}
	if _, err = admin.Exec(ctx, `UPDATE product.mission_focuses SET mission_id=NULL,focus_version=4 WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	obsoleteCommandID := "b8000000-0000-4000-8000-000000000030"
	obsoleteDocument := commandDocument
	obsoleteDocument.CorrelationID = "b8000000-0000-4000-8000-000000000031"
	obsoletePayload, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: obsoleteCommandID, Class: missionCommandClass, ContentType: "application/json"}, mustJSON(t, obsoleteDocument))
	if err != nil {
		t.Fatal(err)
	}
	obsolete, err := dispatcher.Dispatch(ctx, eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: obsoleteCommandID, CommandType: "GenerateDailyTask", AggregateKind: "mission", AggregateID: missionID, PayloadRef: obsoletePayload.Ref, PayloadHash: obsoletePayload.Hash})
	var superseded, taskCount int
	queryErr = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.daily_task_generations WHERE tenant_id=$1 AND planner_command_id=$2 AND status='superseded' AND failure_reason='focus_or_route_changed'),(SELECT count(*) FROM product.daily_tasks WHERE tenant_id=$1 AND user_id=$3)`, tenantID, obsoleteCommandID, userID).Scan(&superseded, &taskCount)
	if err != nil || queryErr != nil || !obsolete.Completed || !obsolete.Superseded || obsolete.RunID != "" || superseded != 1 || taskCount != 1 {
		t.Fatalf("obsolete=%#v superseded=%d tasks=%d err=%v query=%v", obsolete, superseded, taskCount, err, queryErr)
	}
}

type recordingReminderSender struct {
	deliveries []productreminder.Delivery
	err        error
}

func (sender *recordingReminderSender) Send(_ context.Context, delivery productreminder.Delivery) error {
	sender.deliveries = append(sender.deliveries, delivery)
	return sender.err
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
