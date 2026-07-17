//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

func TestProjectStoreEnforcesExactCreateJourneyAndCompletionManifest(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	const (
		userID      = "c6000000-0000-4000-8000-000000000001"
		tenantID    = "c6000000-0000-4000-8000-000000000002"
		roleID      = "c6000000-0000-4000-8000-000000000003"
		missionID   = "c6000000-0000-4000-8000-000000000004"
		routeID     = "c6000000-0000-4000-8000-000000000005"
		projectID   = "c6000000-0000-4000-8000-000000000006"
		bindingID   = "c6000000-0000-4000-8000-000000000007"
		workspaceID = "c6000000-0000-4000-8000-000000000008"
		milestone1  = "c6000000-0000-4000-8000-000000000009"
		milestone2  = "c6000000-0000-4000-8000-000000000010"
		evidenceID  = "c6000000-0000-4000-8000-000000000011"
		testRun1    = "c6000000-0000-4000-8000-000000000012"
		testRun2    = "c6000000-0000-4000-8000-000000000013"
		artifactID  = "c6000000-0000-4000-8000-000000000014"
		revisionID  = "c6000000-0000-4000-8000-000000000015"
		epoch       = "c6000000-0000-4000-8000-000000000016"
	)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'create-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Create Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'create-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'claims-c6')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','claims-c6',0,'{}','encrypted://route','route@c6','ontology@c6','content@c6',$5)`, []any{routeID, tenantID, userID, missionID, now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE id=$2`, []any{routeID, missionID}},
		{`INSERT INTO product.evidence(id,tenant_id,user_id,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,$4,'project','verified','project','encrypted://evidence','evidence-hash-c6',$5)`, []any{evidenceID, tenantID, userID, missionID, now}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	key := bytes.Repeat([]byte{0xc6}, 32)
	actor := json.RawMessage(`{"kind":"user"}`)
	pointer := func(name string) PayloadPointer {
		return PayloadPointer{Ref: "encrypted://create/" + name, Hash: "event-" + name}
	}
	store := ProjectStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	created, err := store.Create(ctx, CreateProjectCommand{MutationID: "create-project-c6", ProjectID: projectID, TenantID: tenantID, UserID: userID, MissionID: missionID, RouteRevisionID: routeID, ProjectKind: "writing", Title: "Production Create journey", CorrelationID: routeID, Brief: PayloadPointer{Ref: "encrypted://brief", Hash: createFixtureHash(projectID, "brief")}, BriefManifestHash: createFixtureHash(projectID, "brief-manifest"), Actor: actor, CreatedEvent: pointer("project-created")})
	if err != nil || created.Version != 1 || created.Status != "active" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	workspace, err := store.BindWorkspace(ctx, BindProjectWorkspaceCommand{MutationID: "bind-workspace-c6", BindingID: bindingID, ProjectID: projectID, TenantID: tenantID, UserID: userID, WorkspaceID: workspaceID, BranchName: "project/c6", BaseRevision: "git:c6", ExpectedProjectVersion: 1, CorrelationID: routeID, Actor: actor, BoundEvent: pointer("workspace-bound")})
	if err != nil || workspace.Version != 2 || workspace.BindingVersion != 1 || workspace.ManifestHash == "" {
		t.Fatalf("workspace=%#v err=%v", workspace, err)
	}
	createMilestone := func(id string, sequence int, expected uint64) MilestoneMutationResult {
		result, createErr := store.CreateMilestone(ctx, CreateMilestoneCommand{MutationID: "create-" + id, MilestoneID: id, ProjectID: projectID, TenantID: tenantID, UserID: userID, ExpectedProjectVersion: expected, Sequence: sequence, Required: true, Title: "Required milestone", AcceptanceSpec: json.RawMessage(`{"schema_version":1,"must_pass":true}`), CorrelationID: routeID, Actor: actor, CreatedEvent: pointer("milestone-created-" + id)})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return result
	}
	if result := createMilestone(milestone1, 1, 2); result.Version != 3 {
		t.Fatalf("milestone1 create=%#v", result)
	}
	if result := createMilestone(milestone2, 2, 3); result.Version != 4 {
		t.Fatalf("milestone2 create=%#v", result)
	}
	reflection := PayloadPointer{Ref: "encrypted://reflection", Hash: createFixtureHash(projectID, "reflection")}
	_, err = store.Complete(ctx, CompleteProjectCommand{MutationID: "complete-too-early-c6", ProjectID: projectID, TenantID: tenantID, UserID: userID, ExpectedProjectVersion: 4, WorkspaceRevision: "git:c6", CorrelationID: routeID, Reflection: reflection, ReflectionManifestHash: createFixtureHash(projectID, "reflection-manifest"), Actor: actor, CompletedEvent: pointer("complete-too-early")})
	if !errors.Is(err, ErrProjectIncomplete) {
		t.Fatalf("early completion error=%v", err)
	}
	transition := func(id, action string, expectedProject, expectedMilestone uint64, result PayloadPointer) MilestoneMutationResult {
		value, transitionErr := store.TransitionMilestone(ctx, TransitionMilestoneCommand{MutationID: action + "-" + id + "-" + strconv.FormatUint(expectedMilestone, 10), MilestoneID: id, ProjectID: projectID, TenantID: tenantID, UserID: userID, Action: action, ExpectedProjectVersion: expectedProject, ExpectedMilestoneVersion: expectedMilestone, Result: result, CorrelationID: routeID, Actor: actor, ChangedEvent: pointer(action + "-" + id)})
		if transitionErr != nil {
			t.Fatalf("%s %s: %v", id, action, transitionErr)
		}
		return value
	}
	empty := PayloadPointer{}
	resultPointer := func(id string) PayloadPointer {
		return PayloadPointer{Ref: "encrypted://result/" + id, Hash: createFixtureHash(id, "result")}
	}
	if value := transition(milestone1, "start", 4, 1, empty); value.Version != 5 || value.MilestoneVersion != 2 {
		t.Fatalf("m1 start=%#v", value)
	}
	transition(milestone1, "submit", 5, 2, resultPointer(milestone1))
	record := func(id, milestone string, expected uint64) ProjectTestRunResult {
		value, recordErr := store.RecordTestRun(ctx, RecordProjectTestRunCommand{MutationID: "record-" + id, TestRunID: id, ProjectID: projectID, MilestoneID: milestone, TenantID: tenantID, UserID: userID, WorkspaceRevision: "git:c6", ValidationKind: "rubric_review", Result: "passed", EvidenceID: evidenceID, CorrelationID: routeID, ExpectedProjectVersion: expected, ResultManifest: json.RawMessage(`{"schema_version":1,"passed":true}`), Actor: json.RawMessage(`{"kind":"service","component":"evaluator"}`), RecordedEvent: pointer("test-recorded-" + id)})
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		return value
	}
	if value := record(testRun1, milestone1, 6); value.Version != 7 {
		t.Fatalf("test1=%#v", value)
	}
	transition(milestone1, "verify", 7, 3, empty)
	transition(milestone1, "complete", 8, 4, empty)
	transition(milestone2, "start", 9, 1, empty)
	transition(milestone2, "submit", 10, 2, resultPointer(milestone2))
	record(testRun2, milestone2, 11)
	transition(milestone2, "verify", 12, 3, empty)
	transition(milestone2, "complete", 13, 4, empty)
	artifactStore := ArtifactStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	if _, err = artifactStore.CreateArtifact(ctx, CreateArtifactCommand{ArtifactID: artifactID, TenantID: tenantID, UserID: userID, ProjectID: projectID, ArtifactKind: "writing", Title: "Final artifact", CorrelationID: routeID, Actor: actor, CreatedEvent: pointer("artifact-created")}); err != nil {
		t.Fatal(err)
	}
	revision, err := artifactStore.AppendArtifactRevision(ctx, AppendArtifactRevisionCommand{ArtifactID: artifactID, RevisionID: revisionID, TenantID: tenantID, UserID: userID, ExpectedArtifactVersion: 1, ContentHash: "artifact-content-c6", ObjectRef: "s3://artifacts/c6", ObjectVersion: "object-version-c6", WorkspaceRevision: "git:c6", MediaType: "text/markdown", ByteSize: 2048, ScanResultHash: "scan-passed-c6", Evidence: []EvidenceBinding{{EvidenceID: evidenceID, Version: 1, ContentHash: "evidence-hash-c6"}}, CorrelationID: routeID, Actor: json.RawMessage(`{"kind":"service"}`), RevisionCreatedEvent: pointer("artifact-revision")})
	if err != nil || revision.Revision != 1 {
		t.Fatalf("revision=%#v err=%v", revision, err)
	}
	completed, err := store.Complete(ctx, CompleteProjectCommand{MutationID: "complete-project-c6", ProjectID: projectID, TenantID: tenantID, UserID: userID, ExpectedProjectVersion: 14, WorkspaceRevision: "git:c6", CorrelationID: routeID, Reflection: reflection, ReflectionManifestHash: createFixtureHash(projectID, "reflection-manifest"), Actor: actor, CompletedEvent: pointer("project-completed")})
	if err != nil || completed.Version != 15 || completed.Status != "completed" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	var manifest json.RawMessage
	var manifestHash, lastEvent, completionEvent string
	var projectEvents, milestones, tests int
	err = admin.QueryRow(ctx, `SELECT completion_manifest,completion_manifest_hash,last_event_id::text,completion_event_id::text,(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='project' AND aggregate_id=$2),(SELECT count(*) FROM product.project_milestones WHERE tenant_id=$1 AND project_id=$2 AND status='completed'),(SELECT count(*) FROM product.project_test_runs WHERE tenant_id=$1 AND project_id=$2 AND result='passed') FROM product.projects WHERE tenant_id=$1 AND id=$2`, tenantID, projectID).Scan(&manifest, &manifestHash, &lastEvent, &completionEvent, &projectEvents, &milestones, &tests)
	if err != nil || len(manifest) == 0 || !sha256Pattern.MatchString(manifestHash) || lastEvent != completionEvent || projectEvents != 15 || milestones != 2 || tests != 2 {
		t.Fatalf("manifest=%s hash=%s events=%d milestones=%d tests=%d last=%s completion=%s err=%v", manifest, manifestHash, projectEvents, milestones, tests, lastEvent, completionEvent, err)
	}
	if _, err = artifactStore.CreateArtifact(ctx, CreateArtifactCommand{ArtifactID: "c6000000-0000-4000-8000-000000000017", TenantID: tenantID, UserID: userID, ProjectID: projectID, ArtifactKind: "writing", Title: "Late mutation", CorrelationID: routeID, Actor: actor, CreatedEvent: pointer("late-artifact")}); !errors.Is(err, ErrArtifactNotWritable) {
		t.Fatalf("late artifact error=%v", err)
	}
}
