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

	behaviorpostgres "github.com/langshift/lites/internal/behavior/postgres"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/integrationfixture"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestPortfolioExportAtomicallyBindsManifestRunAndIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	const (
		userID          = "b5000000-0000-4000-8000-000000000001"
		tenantID        = "b5000000-0000-4000-8000-000000000002"
		roleID          = "b5000000-0000-4000-8000-000000000003"
		missionID       = "b5000000-0000-4000-8000-000000000004"
		routeID         = "b5000000-0000-4000-8000-000000000005"
		projectID       = "b5000000-0000-4000-8000-000000000006"
		evidenceID      = "b5000000-0000-4000-8000-000000000007"
		artifactID      = "b5000000-0000-4000-8000-000000000008"
		revisionID      = "b5000000-0000-4000-8000-000000000009"
		workspaceID     = "b5000000-0000-4000-8000-000000000010"
		bindingID       = "b5000000-0000-4000-8000-000000000011"
		epoch           = "b5000000-0000-4000-8000-000000000012"
		conversation    = "b5000000-0000-4000-8000-000000000013"
		otherEvidenceID = "b5000000-0000-4000-8000-000000000018"
	)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'portfolio-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Portfolio Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'portfolio-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'claims-b5')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','claims-b5',0,'{}','encrypted://route','route@b5','ontology@b5','content@b5',$5)`, []any{routeID, tenantID, userID, missionID, now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE id=$2`, []any{routeID, missionID}},
		{`INSERT INTO product.projects(id,tenant_id,user_id,mission_id,accepted_route_revision_id,status,project_kind,title,brief_ref) VALUES($1,$2,$3,$4,$5,'active','writing','Portfolio Project','encrypted://brief')`, []any{projectID, tenantID, userID, missionID, routeID}},
		{`INSERT INTO product.evidence(id,tenant_id,user_id,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,$4,'project','verified','project','encrypted://evidence','evidence-hash-b5',$5)`, []any{evidenceID, tenantID, userID, missionID, now}},
		{`INSERT INTO product.evidence(id,tenant_id,user_id,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,$4,'reflection','verified','project','encrypted://other-evidence','other-evidence-hash-b5',$5)`, []any{otherEvidenceID, tenantID, userID, missionID, now}},
		{`INSERT INTO product.project_workspace_bindings(id,tenant_id,user_id,project_id,workspace_id,branch_name,base_revision,head_revision,binding_manifest_hash) VALUES($1,$2,$3,$4,$5,'project/b5','git:base','git:head','workspace-manifest-b5')`, []any{bindingID, tenantID, userID, projectID, workspaceID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := integrationfixture.SeedBehaviorChannel(ctx, admin, tenantID, userID, "artifact_builder", "production", now); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0xb5}, 32)
	appender := eventpostgres.Appender{Now: func() time.Time { return now }}
	artifactStore := ArtifactStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	created, err := artifactStore.CreateArtifact(ctx, CreateArtifactCommand{ArtifactID: artifactID, TenantID: tenantID, UserID: userID, ProjectID: projectID, ArtifactKind: "writing", Title: "Exported portfolio", CorrelationID: routeID, Actor: json.RawMessage(`{"kind":"user"}`), CreatedEvent: PayloadPointer{Ref: "encrypted://artifact/created", Hash: "artifact-created-b5"}})
	if err != nil || created.Version != 1 {
		t.Fatalf("artifact create=%#v err=%v", created, err)
	}
	revision, err := artifactStore.AppendArtifactRevision(ctx, AppendArtifactRevisionCommand{ArtifactID: artifactID, RevisionID: revisionID, TenantID: tenantID, UserID: userID, ExpectedArtifactVersion: 1, ContentHash: "artifact-content-b5", ObjectRef: "s3://artifacts/b5", ObjectVersion: "object-version-b5", WorkspaceRevision: "git:head", MediaType: "text/markdown", ByteSize: 4096, ScanResultHash: "scan-passed-b5", Evidence: []EvidenceBinding{{EvidenceID: evidenceID, Version: 1, ContentHash: "evidence-hash-b5"}}, CorrelationID: routeID, Actor: json.RawMessage(`{"kind":"service"}`), RevisionCreatedEvent: PayloadPointer{Ref: "encrypted://artifact/revision", Hash: "artifact-revision-b5"}})
	if err != nil || revision.Revision != 1 {
		t.Fatalf("artifact revision=%#v err=%v", revision, err)
	}
	if _, err = admin.Exec(ctx, `UPDATE product.projects SET version=2,status='completed',reflection_ref='encrypted://reflection',completed_at=$1,updated_at=$1 WHERE id=$2`, now, projectID); err != nil {
		t.Fatal(err)
	}
	behaviorResolver := behaviorpostgres.Store{}
	store := PortfolioExportStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }, Behavior: behaviorResolver}
	runTokens := opaque.Manager{Purpose: "portfolio-builder-run", Pepper: bytes.Repeat([]byte{0xb6}, 32)}
	store.RunTokens = runTokens
	runStore := executionpostgres.RunStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Tokens: runTokens, LeaseTTL: 2 * time.Minute, Now: func() time.Time { return now }, Behavior: behaviorResolver}
	makeCommand := func(exportID, requestID, runID string) RequestPortfolioExportCommand {
		correlationID := exportID
		return RequestPortfolioExportCommand{
			ExportID: exportID, RequestID: requestID, TenantID: tenantID, UserID: userID, ProjectID: projectID, ProjectVersion: 2, CorrelationID: correlationID,
			Workspace: WorkspaceExportBinding{BindingID: bindingID, Version: 1, Revision: "git:head", ManifestHash: "workspace-manifest-b5"}, Format: "pdf",
			Artifacts: []PortfolioArtifactBinding{{RevisionID: revisionID, ArtifactID: artifactID, Revision: 1, ContentHash: "artifact-content-b5", ObjectVersion: "object-version-b5", WorkspaceRevision: "git:head", MediaType: "text/markdown", ByteSize: 4096, ScanResultHash: "scan-passed-b5", EvidenceManifestHash: revision.EvidenceManifestHash}}, Evidence: []EvidenceBinding{{EvidenceID: evidenceID, Version: 1, ContentHash: "evidence-hash-b5"}},
			Actor: json.RawMessage(`{"kind":"user"}`), RequestedEvent: PayloadPointer{Ref: "encrypted://portfolio/" + exportID, Hash: "portfolio-requested-" + exportID},
			Run: executionpostgres.AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversation, CorrelationID: correlationID, DueAt: now.Add(time.Hour), BehaviorProfile: "artifact_builder", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16,"max_cost_microunits":100000}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/accepted/" + runID, Hash: "run-accepted-" + runID}, QueuedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/queued/" + runID, Hash: "run-queued-" + runID}, StartCommand: executionpostgres.PayloadPointer{Ref: "encrypted://run/start/" + runID, Hash: "run-start-" + runID}, QueueClass: "background", ResourceClass: "llm", Priority: 50, CostUnits: 8, MaxAttempts: 5},
		}
	}
	command := makeCommand("b5000000-0000-4000-8000-000000000014", "portfolio-request-b5", "b5000000-0000-4000-8000-000000000015")
	requested, err := store.Request(ctx, command)
	if err != nil || requested.Status != "requested" || requested.Version != 1 || requested.RunID != command.Run.RunID || requested.StartCommandID == "" || requested.Replayed || requested.RevisionManifestHash == "" {
		t.Fatalf("requested=%#v err=%v", requested, err)
	}
	claimRun := func(request RequestPortfolioExportCommand, startCommandID, suffix string) executionpostgres.RunClaim {
		claim, claimErr := runStore.ClaimStart(ctx, executionpostgres.ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: startCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: request.Run.RunID, PayloadRef: request.Run.StartCommand.Ref, PayloadHash: request.Run.StartCommand.Hash}, ConsumerName: "portfolio-builder", WorkerID: "portfolio-worker-b5", CorrelationID: request.CorrelationID, Actor: json.RawMessage(`{"kind":"service"}`), RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/started/" + suffix, Hash: "run-started-" + suffix}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://attempt/started/" + suffix, Hash: "attempt-started-" + suffix}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://attempt/expired/" + suffix, Hash: "attempt-expired-" + suffix}})
		if claimErr != nil {
			t.Fatalf("claim %s: %v", suffix, claimErr)
		}
		return claim
	}
	claim := claimRun(command, requested.StartCommandID, "success")
	now = now.Add(3 * time.Minute)
	recoveryTenants, err := store.ListRecoverableTenantIDs(ctx, PortfolioRecoveryTenantScan{Limit: 100, ShardCount: 1, StaleAfter: time.Minute})
	if err != nil || len(recoveryTenants) != 1 || recoveryTenants[0] != tenantID {
		t.Fatalf("requested recovery tenants=%v err=%v", recoveryTenants, err)
	}
	recoveryCandidates, err := store.ListRecoveryCandidates(ctx, PortfolioRecoveryCandidateScan{TenantID: tenantID, Limit: 100, StaleAfter: time.Minute})
	if err != nil || len(recoveryCandidates) != 1 || recoveryCandidates[0].ExportID != command.ExportID || recoveryCandidates[0].ExportStatus != "requested" || recoveryCandidates[0].Disposition != PortfolioRecoveryExpiredRedelivery {
		t.Fatalf("requested recovery candidates=%#v err=%v", recoveryCandidates, err)
	}
	recoveredBeforeStart := claimRun(command, requested.StartCommandID, "recovered-before-start")
	if recoveredBeforeStart.Fence != claim.Fence+1 || recoveredBeforeStart.AttemptID == claim.AttemptID {
		t.Fatalf("first recovery did not replace execution right: old=%#v new=%#v", claim, recoveredBeforeStart)
	}
	claim = recoveredBeforeStart
	startedCommand := StartPortfolioBuildCommand{ExportID: command.ExportID, TenantID: tenantID, UserID: userID, ExpectedExportVersion: 1, Claim: claim, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: command.CorrelationID, BuildStartedEvent: PayloadPointer{Ref: "encrypted://portfolio/started/success", Hash: "portfolio-started-success"}}
	started, err := store.StartBuild(ctx, startedCommand)
	if err != nil || started.Status != "building" || started.Version != 2 || started.Replayed {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	startedReplay, err := store.StartBuild(ctx, startedCommand)
	if err != nil || !startedReplay.Replayed || startedReplay.Status != "building" || startedReplay.Version != 2 {
		t.Fatalf("start replay=%#v err=%v", startedReplay, err)
	}
	now = now.Add(3 * time.Minute)
	recoveryCandidates, err = store.ListRecoveryCandidates(ctx, PortfolioRecoveryCandidateScan{TenantID: tenantID, Limit: 100, StaleAfter: time.Minute})
	if err != nil || len(recoveryCandidates) != 1 || recoveryCandidates[0].ExportID != command.ExportID || recoveryCandidates[0].ExportStatus != "building" || recoveryCandidates[0].Disposition != PortfolioRecoveryExpiredRedelivery {
		t.Fatalf("building recovery candidates=%#v err=%v", recoveryCandidates, err)
	}
	recoveredAfterStart := claimRun(command, requested.StartCommandID, "recovered-after-start")
	if recoveredAfterStart.Fence != claim.Fence+1 || recoveredAfterStart.AttemptID == claim.AttemptID {
		t.Fatalf("second recovery did not replace execution right: old=%#v new=%#v", claim, recoveredAfterStart)
	}
	claim = recoveredAfterStart
	startedCommand.Claim = claim
	startedAfterRecovery, err := store.StartBuild(ctx, startedCommand)
	if err != nil || !startedAfterRecovery.Replayed || startedAfterRecovery.Status != "building" || startedAfterRecovery.Version != 2 || !startedAfterRecovery.StartedAt.Equal(started.StartedAt) {
		t.Fatalf("recovered start replay=%#v err=%v", startedAfterRecovery, err)
	}
	runCompletion := executionpostgres.CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: statemachine.RunSucceeded, ResultHash: "portfolio-content-b5", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: command.CorrelationID, RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/succeeded/success", Hash: "run-succeeded-success"}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://attempt/completed/success", Hash: "attempt-completed-success"}}
	completeCommand := CompletePortfolioExportCommand{ExportID: command.ExportID, TenantID: tenantID, UserID: userID, ExpectedExportVersion: 2, ObjectRef: "s3://portfolio/b5.pdf", ObjectVersion: "portfolio-object-v1", ContentHash: "portfolio-content-b5", MediaType: "application/pdf", ScanResultHash: "portfolio-scan-b5", CorrelationID: command.CorrelationID, ByteSize: 8192, ExpiresAt: now.Add(30 * 24 * time.Hour), Actor: json.RawMessage(`{"kind":"service"}`), CompletedEvent: PayloadPointer{Ref: "encrypted://portfolio/completed/success", Hash: "portfolio-completed-success"}, RunCompletion: runCompletion}
	completed, err := store.Complete(ctx, completeCommand)
	if err != nil || completed.Status != "ready" || completed.Version != 3 || completed.Replayed || completed.ContentHash != "portfolio-content-b5" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	recoveryCandidates, err = store.ListRecoveryCandidates(ctx, PortfolioRecoveryCandidateScan{TenantID: tenantID, Limit: 100, StaleAfter: time.Minute})
	if err != nil || len(recoveryCandidates) != 0 {
		t.Fatalf("terminal export remained recoverable: %#v err=%v", recoveryCandidates, err)
	}
	completedReplay, err := store.Complete(ctx, completeCommand)
	if err != nil || !completedReplay.Replayed || completedReplay.CompletionEventID != completed.CompletionEventID {
		t.Fatalf("completion replay=%#v err=%v", completedReplay, err)
	}
	startedAfterCompletion, err := store.StartBuild(ctx, startedCommand)
	if err != nil || !startedAfterCompletion.Replayed || startedAfterCompletion.Status != "ready" {
		t.Fatalf("late start replay=%#v err=%v", startedAfterCompletion, err)
	}
	replayed, err := store.Request(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Status != "ready" || replayed.Version != 3 || replayed.StartCommandID != requested.StartCommandID {
		t.Fatalf("advanced replay=%#v err=%v", replayed, err)
	}
	substitution := command
	substitution.Format = "html"
	if _, err = store.Request(ctx, substitution); !errors.Is(err, ErrPortfolioExportConflict) {
		t.Fatalf("manifest substitution error=%v", err)
	}
	now = now.Add(31 * 24 * time.Hour)
	expirableTenants, err := store.ListExpirableTenants(ctx, PortfolioExpiryTenantScan{Limit: 100, ShardCount: 1, Now: now})
	if err != nil || len(expirableTenants) != 1 || expirableTenants[0] != tenantID {
		t.Fatalf("expirable tenants=%v err=%v", expirableTenants, err)
	}
	expireCommand := ExpirePortfolioExportCommand{ExportID: command.ExportID, TenantID: tenantID, UserID: userID, ExpectedExportVersion: 3, ExpiredAt: now, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: command.CorrelationID, ExpiredEvent: PayloadPointer{Ref: "encrypted://portfolio/expired/success", Hash: "portfolio-expired-success"}}
	expired, err := store.Expire(ctx, expireCommand)
	if err != nil || expired.Status != "expired" || expired.Version != 4 || expired.Replayed {
		t.Fatalf("expired=%#v err=%v", expired, err)
	}
	expiredReplay, err := store.Expire(ctx, expireCommand)
	if err != nil || !expiredReplay.Replayed || expiredReplay.ExpiryEventID != expired.ExpiryEventID {
		t.Fatalf("expiry replay=%#v err=%v", expiredReplay, err)
	}
	omittedProvenance := makeCommand("b5000000-0000-4000-8000-000000000019", "portfolio-request-b5-omitted", "b5000000-0000-4000-8000-000000000020")
	omittedProvenance.Evidence = []EvidenceBinding{{EvidenceID: otherEvidenceID, Version: 1, ContentHash: "other-evidence-hash-b5"}}
	if _, err = store.Request(ctx, omittedProvenance); !errors.Is(err, ErrPortfolioBinding) {
		t.Fatalf("omitted artifact provenance error=%v", err)
	}
	var orphanRun int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=$2`, tenantID, omittedProvenance.Run.RunID).Scan(&orphanRun); err != nil || orphanRun != 0 {
		t.Fatalf("orphan run=%d err=%v", orphanRun, err)
	}

	concurrent := makeCommand("b5000000-0000-4000-8000-000000000016", "portfolio-request-b5-concurrent", "b5000000-0000-4000-8000-000000000017")
	const workers = 16
	var wait sync.WaitGroup
	var fresh, retries atomic.Int64
	errorsFound := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, requestErr := store.Request(ctx, concurrent)
			if requestErr != nil {
				errorsFound <- requestErr
				return
			}
			if result.Replayed {
				retries.Add(1)
			} else {
				fresh.Add(1)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for requestErr := range errorsFound {
		t.Fatalf("concurrent request: %v", requestErr)
	}
	if fresh.Load() != 1 || retries.Load() != workers-1 {
		t.Fatalf("fresh=%d retries=%d", fresh.Load(), retries.Load())
	}
	concurrentAccepted, err := store.Request(ctx, concurrent)
	if err != nil || !concurrentAccepted.Replayed {
		t.Fatalf("load concurrent export=%#v err=%v", concurrentAccepted, err)
	}
	failedClaim := claimRun(concurrent, concurrentAccepted.StartCommandID, "failure")
	failedStart := StartPortfolioBuildCommand{ExportID: concurrent.ExportID, TenantID: tenantID, UserID: userID, ExpectedExportVersion: 1, Claim: failedClaim, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: concurrent.CorrelationID, BuildStartedEvent: PayloadPointer{Ref: "encrypted://portfolio/started/failure", Hash: "portfolio-started-failure"}}
	if _, err = store.StartBuild(ctx, failedStart); err != nil {
		t.Fatalf("start failed export: %v", err)
	}
	failCommand := FailPortfolioExportCommand{ExportID: concurrent.ExportID, TenantID: tenantID, UserID: userID, ExpectedExportVersion: 2, FailureCode: "renderer_failed", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: concurrent.CorrelationID, FailedEvent: PayloadPointer{Ref: "encrypted://portfolio/failed/failure", Hash: "portfolio-failed-failure"}, RunCompletion: executionpostgres.CompleteRunCommand{Claim: failedClaim, ExpectedRunVersion: failedClaim.RunVersion, TargetState: statemachine.RunFailed, ResultHash: "portfolio-failure-b5", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: concurrent.CorrelationID, RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://run/failed/failure", Hash: "run-failed-failure"}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://attempt/completed/failure", Hash: "attempt-completed-failure"}}}
	failed, err := store.Fail(ctx, failCommand)
	if err != nil || failed.Status != "failed" || failed.Version != 3 || failed.Replayed || failed.FailureCode != "renderer_failed" {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	failedReplay, err := store.Fail(ctx, failCommand)
	if err != nil || !failedReplay.Replayed || failedReplay.CompletionEventID != failed.CompletionEventID {
		t.Fatalf("failure replay=%#v err=%v", failedReplay, err)
	}
	var exports, artifactLinks, evidenceLinks, runs, jobs, runEvents, requestEvents, startCommands int
	var requestedSchema, completedSchema int
	var successfulVersions, failedVersions string
	err = admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM product.portfolio_exports WHERE tenant_id=$1),
		(SELECT count(*) FROM product.portfolio_export_artifacts WHERE tenant_id=$1),
		(SELECT count(*) FROM product.portfolio_export_evidence WHERE tenant_id=$1),
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id IN ($2,$3)),
		(SELECT count(*) FROM agent.jobs WHERE tenant_id=$1),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id IN ($2,$3)),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='portfolio_export'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='StartAgentRun' AND aggregate_id IN ($2,$3)),
		(SELECT event_schema_version FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='portfolio_export' AND aggregate_id=$4 AND event_type='PortfolioExportRequested'),
		(SELECT event_schema_version FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='portfolio_export' AND aggregate_id=$4 AND event_type='PortfolioExportCompleted'),
		(SELECT array_agg(aggregate_version ORDER BY aggregate_version)::text FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='portfolio_export' AND aggregate_id=$4),
		(SELECT array_agg(aggregate_version ORDER BY aggregate_version)::text FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='portfolio_export' AND aggregate_id=$5)`, tenantID, command.Run.RunID, concurrent.Run.RunID, command.ExportID, concurrent.ExportID).Scan(&exports, &artifactLinks, &evidenceLinks, &runs, &jobs, &runEvents, &requestEvents, &startCommands, &requestedSchema, &completedSchema, &successfulVersions, &failedVersions)
	if err != nil || exports != 2 || artifactLinks != 2 || evidenceLinks != 2 || runs != 2 || jobs != 2 || runEvents != 8 || requestEvents != 7 || startCommands != 2 || requestedSchema != 2 || completedSchema != 2 || successfulVersions != "{1,2,3,4}" || failedVersions != "{1,2,3}" {
		t.Fatalf("exports=%d artifact_links=%d evidence_links=%d runs=%d jobs=%d run_events=%d portfolio_events=%d starts=%d schemas=%d/%d versions=%s/%s err=%v", exports, artifactLinks, evidenceLinks, runs, jobs, runEvents, requestEvents, startCommands, requestedSchema, completedSchema, successfulVersions, failedVersions, err)
	}
}
