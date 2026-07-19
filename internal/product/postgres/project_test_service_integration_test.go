//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

func TestProjectTestGenerationAtomicallyPinsExactProjectWorkspaceAndEvaluator(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 20, 0, 0, 0, time.UTC)
	const (
		userID      = "f6000000-0000-4000-8000-000000000001"
		tenantID    = "f6000000-0000-4000-8000-000000000002"
		roleID      = "f6000000-0000-4000-8000-000000000003"
		missionID   = "f6000000-0000-4000-8000-000000000004"
		routeID     = "f6000000-0000-4000-8000-000000000005"
		workspaceID = "f6000000-0000-4000-8000-000000000006"
		epoch       = "f6000000-0000-4000-8000-000000000007"
		sessionID   = "f6000000-0000-4000-8000-000000000008"
	)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'project-test-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Project Test Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'project-test-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'claims-f6')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','claims-f6',0,'{}','encrypted://route','route@f6','ontology@f6','content@f6',$5)`, []any{routeID, tenantID, userID, missionID, now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE id=$2`, []any{routeID, missionID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	binding, err := integrationfixture.SeedBehaviorChannel(ctx, admin, tenantID, userID, behavior.Evaluator, "production", now)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0xf6}, 32)
	values := &missionPayloadStore{values: map[string][]byte{}}
	epochs := artifactEpochStub{epoch}
	appender := eventAppender(now)
	projectStore := ProjectStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: epochs, Now: func() time.Time { return now }}
	projects := ProjectApplicationService{Pool: pool, Store: projectStore, Payloads: values, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xf7}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xf8}, 32), CursorKey: bytes.Repeat([]byte{0xf9}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	metadata := func(client, keyValue string) productapi.CommandMetadata {
		return productapi.CommandMetadata{RequestID: createFixtureUUID(client, "gateway"), ClientRequestID: client, IdempotencyKey: keyValue, TenantID: tenantID, UserID: userID, SessionID: sessionID}
	}
	created, err := projects.Create(ctx, productapi.CreateProjectCommand{CommandMetadata: metadata("project-test-create-f6", "project-test-create-idempotency-f6"), MissionID: missionID, RouteRevisionID: routeID, ProjectKind: "code", Title: "Evaluator project", Brief: "Build a production-grade exact output and prove every acceptance condition."})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := projects.BindWorkspace(ctx, productapi.BindWorkspaceCommand{CommandMetadata: metadata("project-test-workspace-f6", "project-test-workspace-idempotency-f6"), ProjectID: created.ID, WorkspaceID: workspaceID, BranchName: "project/f6", BaseRevision: "git:exact-f6", ExpectedProjectVersion: created.Version})
	if err != nil {
		t.Fatal(err)
	}
	milestone, err := projects.CreateMilestone(ctx, productapi.CreateMilestoneCommand{CommandMetadata: metadata("project-test-milestone-f6", "project-test-milestone-idempotency-f6"), ProjectID: created.ID, ExpectedProjectVersion: workspace.Version, Sequence: 1, Required: true, Title: "Production acceptance", AcceptanceSpec: json.RawMessage(`{"checks":["exact output","test evidence"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	started, err := projects.TransitionMilestone(ctx, productapi.TransitionMilestoneCommand{CommandMetadata: metadata("project-test-start-f6", "project-test-start-idempotency-f6"), ProjectID: created.ID, MilestoneID: milestone.MilestoneID, Action: "start", ExpectedProjectVersion: milestone.Version, ExpectedMilestoneVersion: milestone.MilestoneVersion})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := projects.TransitionMilestone(ctx, productapi.TransitionMilestoneCommand{CommandMetadata: metadata("project-test-submit-f6", "project-test-submit-idempotency-f6"), ProjectID: created.ID, MilestoneID: milestone.MilestoneID, Action: "submit", Result: "The exact workspace output is ready for independent evaluation.", ExpectedProjectVersion: started.Version, ExpectedMilestoneVersion: started.MilestoneVersion})
	if err != nil {
		t.Fatal(err)
	}
	behaviorStore := behaviorpostgres.Store{Pool: pool, Appender: appender, StoreEpoch: epoch, Epochs: epochs, Now: func() time.Time { return now }}
	runs := executionpostgres.RunStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: epochs, Behavior: behaviorStore, Now: func() time.Time { return now }}
	service := ProjectTestGenerationService{Pool: pool, Runs: runs, Payloads: values, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xfa}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xfb}, 32), BehaviorEnvironment: "production", RunTimeout: 30 * time.Minute, RunMaxSteps: 12, RunMaxCostMicrounits: 100000, RunMaxAttempts: 5, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	command := productapi.GenerateProjectTestCommand{CommandMetadata: metadata("project-test-generate-f6", "project-test-generate-idempotency-f6"), ProjectID: created.ID, MilestoneID: milestone.MilestoneID, WorkspaceRevision: workspace.Workspace.HeadRevision, ValidationKind: "rubric_review", ExpectedProjectVersion: submitted.Version, ExpectedMilestoneVersion: submitted.MilestoneVersion, ExpectedWorkspaceBindingVersion: workspace.Workspace.Version, ValidationSpec: json.RawMessage(`{"rubric":"all acceptance checks require observed evidence"}`)}
	generated, err := service.Generate(ctx, command)
	if err != nil || generated.Status != "queued" || generated.ProjectVersion != submitted.Version || generated.WorkspaceRevision != workspace.Workspace.HeadRevision || generated.Replayed {
		t.Fatalf("generated=%#v err=%v", generated, err)
	}
	retry := command
	retry.RequestID = createFixtureUUID("project-test-generate-f6", "retry-gateway")
	retry.SessionID = createFixtureUUID("project-test-generate-f6", "retry-session")
	replayed, err := service.Generate(ctx, retry)
	if err != nil || !replayed.Replayed || replayed.GenerationID != generated.GenerationID || replayed.RunID != generated.RunID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	substitution := command
	substitution.ValidationSpec = json.RawMessage(`{"rubric":"substituted"}`)
	if _, err = service.Generate(ctx, substitution); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("substitution err=%v", err)
	}
	var generations, runsCount, conversations, startCommands, idempotencyRows int
	var projectVersion, generationProjectVersion uint64
	var snapshotID, channelID, workspaceRevision string
	var sequence uint64
	err = admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM product.project_test_generations WHERE tenant_id=$1),
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=$2),
		(SELECT count(*) FROM agent.conversations WHERE tenant_id=$1 AND mode='project'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='StartAgentRun' AND aggregate_id=$2),
		(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND operation_id='projects.test.generate.v2' AND status='completed'),
		(SELECT version FROM product.projects WHERE tenant_id=$1 AND id=$3),
		g.project_version,g.behavior_snapshot_id,g.behavior_channel_id::text,g.behavior_channel_sequence,g.workspace_revision
		FROM product.project_test_generations g WHERE g.tenant_id=$1 AND g.id=$4`, tenantID, generated.RunID, created.ID, generated.GenerationID).Scan(&generations, &runsCount, &conversations, &startCommands, &idempotencyRows, &projectVersion, &generationProjectVersion, &snapshotID, &channelID, &sequence, &workspaceRevision)
	if err != nil || generations != 1 || runsCount != 1 || conversations != 1 || startCommands != 1 || idempotencyRows != 1 || projectVersion != submitted.Version || generationProjectVersion != submitted.Version || snapshotID != binding.SnapshotID || channelID != binding.ChannelID || sequence != binding.Sequence || workspaceRevision != workspace.Workspace.HeadRevision {
		t.Fatalf("durable generation=%d runs=%d conversations=%d commands=%d idem=%d project=%d generation_project=%d snapshot=%s channel=%s sequence=%d workspace=%s err=%v", generations, runsCount, conversations, startCommands, idempotencyRows, projectVersion, generationProjectVersion, snapshotID, channelID, sequence, workspaceRevision, err)
	}
	var startCommandID, startRef, startHash string
	if err = admin.QueryRow(ctx, `SELECT command_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id=$2 AND command_type='StartAgentRun'`, tenantID, generated.RunID).Scan(&startCommandID, &startRef, &startHash); err != nil {
		t.Fatal(err)
	}
	startBody, err := values.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: startCommandID, Class: "agent-run-command", ContentType: "application/json"}, payload.Manifest{Ref: startRef, Hash: startHash})
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		CorrelationID string `json:"correlation_id"`
	}
	if json.Unmarshal(startBody, &start) != nil || start.SchemaVersion != 1 || start.RunID != generated.RunID {
		t.Fatalf("start=%s", startBody)
	}
	workerRuns := executionpostgres.RunStore{Pool: admin, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: epochs, Behavior: behaviorpostgres.Store{}, Tokens: opaque.Manager{Purpose: "project-test-run", Pepper: bytes.Repeat([]byte{0xfc}, 32)}, LeaseTTL: 5 * time.Minute, Now: func() time.Time { return now }}
	claim, err := workerRuns.ClaimStart(ctx, executionpostgres.ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: startCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: generated.RunID, PayloadRef: startRef, PayloadHash: startHash}, ConsumerName: "project-evaluator-test", WorkerID: "project-evaluator-worker", CorrelationID: start.CorrelationID, Actor: json.RawMessage(`{"kind":"service"}`), RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://project-test/run-started", Hash: strings.Repeat("1", 64)}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://project-test/attempt-started", Hash: strings.Repeat("2", 64)}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://project-test/attempt-expired", Hash: strings.Repeat("3", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	evaluationJSON := []byte(`{"schema_version":1,"result":"passed","summary":"All exact production acceptance checks passed.","checks":[{"name":"acceptance specification","status":"pass","evidence":"The evaluator observed the required exact output at git:exact-f6."},{"name":"test evidence","status":"pass","evidence":"The pinned evaluator run produced evidence for the exact workspace revision."}],"uncertainty":"No material uncertainty remains for the declared rubric scope."}`)
	messageJSON := mustJSON(t, map[string]any{"schema_version": 1, "role": "assistant", "content": []map[string]any{{"type": "text", "text": string(evaluationJSON)}}})
	messageID := "f6000000-0000-4000-8000-000000000050"
	messagePayload, err := values.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: messageID, Class: "run-message", ContentType: "application/json"}, messageJSON)
	if err != nil {
		t.Fatal(err)
	}
	messageDigest := sha256.Sum256(messageJSON)
	if _, err = workerRuns.CompleteRunWithMessage(ctx, executionpostgres.CompleteRunMessageCommand{Completion: executionpostgres.CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: "succeeded", ResultHash: strings.Repeat("4", 64), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: start.CorrelationID, RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://project-test/run-succeeded", Hash: strings.Repeat("5", 64)}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://project-test/attempt-completed", Hash: strings.Repeat("6", 64)}}, MessageID: messageID, Message: executionpostgres.PayloadPointer{Ref: messagePayload.Ref, Hash: messagePayload.Hash}, ContentHash: hex.EncodeToString(messageDigest[:]), FinalizedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://project-test/message-finalized", Hash: strings.Repeat("7", 64)}}); err != nil {
		t.Fatal(err)
	}
	reconciler := ProjectTestReconciler{Pool: pool, Store: projectStore, Appender: appender, Payloads: values, IDKey: key, StoreEpoch: epoch, Now: func() time.Time { return now }}
	tenants, err := reconciler.ListTenantIDs(ctx, "", 100)
	if err != nil || len(tenants) != 1 || tenants[0] != tenantID {
		t.Fatalf("tenants=%v err=%v", tenants, err)
	}
	reconciled, err := reconciler.ReconcileTenant(ctx, tenantID, 100)
	if err != nil || reconciled.Succeeded != 1 || reconciled.Failed != 0 || reconciled.Superseded != 0 {
		t.Fatalf("reconciled=%#v err=%v", reconciled, err)
	}
	var generationStatus, testResult string
	var finalProjectVersion uint64
	var testRows, evidenceRows, projectEvents, evidenceEvents int
	err = admin.QueryRow(ctx, `SELECT
		g.status,p.version,t.result,
		(SELECT count(*) FROM product.project_test_runs WHERE tenant_id=$1 AND project_id=$2 AND workspace_revision=$3),
		(SELECT count(*) FROM product.evidence WHERE tenant_id=$1 AND source_kind='evaluator_run' AND source_id=$4),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='project' AND aggregate_id=$2 AND event_type='ProjectTestRunRecorded'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='evidence' AND event_type='CapabilityEvidenceRecorded')
		FROM product.project_test_generations g JOIN product.projects p ON p.tenant_id=g.tenant_id AND p.id=g.project_id JOIN product.project_test_runs t ON t.tenant_id=g.tenant_id AND t.id=g.project_test_run_id WHERE g.tenant_id=$1 AND g.id=$5`, tenantID, created.ID, workspace.Workspace.HeadRevision, generated.RunID, generated.GenerationID).Scan(&generationStatus, &finalProjectVersion, &testResult, &testRows, &evidenceRows, &projectEvents, &evidenceEvents)
	if err != nil || generationStatus != "succeeded" || finalProjectVersion != submitted.Version+1 || testResult != "passed" || testRows != 1 || evidenceRows != 1 || projectEvents != 1 || evidenceEvents != 1 {
		t.Fatalf("generation=%s project=%d result=%s tests=%d evidence=%d project_events=%d evidence_events=%d err=%v", generationStatus, finalProjectVersion, testResult, testRows, evidenceRows, projectEvents, evidenceEvents, err)
	}
	snapshot, err := projects.Get(ctx, productapi.ProjectGetQuery{TenantID: tenantID, UserID: userID, ProjectID: created.ID})
	if err != nil || snapshot.Project.Version != finalProjectVersion || snapshot.Brief != "Build a production-grade exact output and prove every acceptance condition." || snapshot.Workspace == nil || snapshot.Workspace.ID != workspace.Workspace.ID || len(snapshot.Milestones) != 1 || snapshot.Milestones[0].Result == nil || *snapshot.Milestones[0].Result != "The exact workspace output is ready for independent evaluation." || len(snapshot.Milestones[0].EvidenceIDs) != 1 || snapshot.Milestones[0].LatestEvaluation == nil || snapshot.Milestones[0].LatestEvaluation.Status != "succeeded" || snapshot.Milestones[0].LatestEvaluation.RunID != generated.RunID {
		t.Fatalf("reconciled recovery snapshot=%#v err=%v", snapshot, err)
	}
	replayedReconcile, err := reconciler.ReconcileTenant(ctx, tenantID, 100)
	if err != nil || replayedReconcile.Scanned != 0 {
		t.Fatalf("reconcile replay=%#v err=%v", replayedReconcile, err)
	}
}
