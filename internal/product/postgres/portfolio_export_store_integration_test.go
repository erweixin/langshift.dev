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

	behaviorpostgres "github.com/langshift/lites/internal/behavior/postgres"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/integrationfixture"
	"github.com/langshift/lites/internal/payload"
	productapi "github.com/langshift/lites/internal/product/api"
	"github.com/langshift/lites/internal/security/opaque"
)

type portfolioFixturePayloadStore struct {
	inner    *missionPayloadStore
	fixtures map[string][]byte
}

func (store portfolioFixturePayloadStore) Put(ctx context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	return store.inner.Put(ctx, descriptor, value)
}

func (store portfolioFixturePayloadStore) Get(ctx context.Context, descriptor payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	if value, found := store.fixtures[manifest.Ref]; found {
		return append([]byte(nil), value...), nil
	}
	return store.inner.Get(ctx, descriptor, manifest)
}

func TestPortfolioExportAtomicallyBindsManifestRunAndIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	agentPool := artifactPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agentPool.Close()
	now := time.Date(2026, time.July, 15, 16, 0, 0, 0, time.UTC)
	const (
		userID            = "b5000000-0000-4000-8000-000000000001"
		tenantID          = "b5000000-0000-4000-8000-000000000002"
		roleID            = "b5000000-0000-4000-8000-000000000003"
		missionID         = "b5000000-0000-4000-8000-000000000004"
		routeID           = "b5000000-0000-4000-8000-000000000005"
		projectID         = "b5000000-0000-4000-8000-000000000006"
		evidenceID        = "b5000000-0000-4000-8000-000000000007"
		artifactID        = "b5000000-0000-4000-8000-000000000008"
		revisionID        = "b5000000-0000-4000-8000-000000000009"
		workspaceID       = "b5000000-0000-4000-8000-000000000010"
		bindingID         = "b5000000-0000-4000-8000-000000000011"
		epoch             = "b5000000-0000-4000-8000-000000000012"
		conversation      = "b5000000-0000-4000-8000-000000000013"
		otherEvidenceID   = "b5000000-0000-4000-8000-000000000018"
		projectEventID    = "b5000000-0000-4000-8000-000000000021"
		workspaceEventID  = "b5000000-0000-4000-8000-000000000022"
		completionEventID = "b5000000-0000-4000-8000-000000000023"
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
		{`INSERT INTO product.evidence(id,tenant_id,user_id,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,$4,'project','verified','project','encrypted://evidence','evidence-hash-b5',$5)`, []any{evidenceID, tenantID, userID, missionID, now}},
		{`INSERT INTO product.evidence(id,tenant_id,user_id,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,$4,'reflection','verified','project','encrypted://other-evidence','other-evidence-hash-b5',$5)`, []any{otherEvidenceID, tenantID, userID, missionID, now}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	projectFixture := activeProjectFixture{ProjectID: projectID, TenantID: tenantID, UserID: userID, MissionID: missionID, RouteID: routeID, ProjectEventID: projectEventID, StoreEpoch: epoch, CorrelationID: routeID, WorkspaceID: workspaceID, BindingID: bindingID, WorkspaceEventID: workspaceEventID, At: now}
	workspaceManifestHash := seedActiveProject(t, ctx, admin, projectFixture)
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
	completeProjectFixture(t, ctx, admin, projectFixture, completionEventID)
	behaviorResolver := behaviorpostgres.Store{}
	store := PortfolioExportStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }, Behavior: behaviorResolver}
	runTokens := opaque.Manager{Purpose: "portfolio-builder-run", Pepper: bytes.Repeat([]byte{0xb6}, 32)}
	store.RunTokens = runTokens
	runStore := executionpostgres.RunStore{Pool: pool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Tokens: runTokens, LeaseTTL: 2 * time.Minute, Now: func() time.Time { return now }, Behavior: behaviorResolver}
	makeCommand := func(exportID, requestID, runID string) RequestPortfolioExportCommand {
		correlationID := exportID
		return RequestPortfolioExportCommand{
			ExportID: exportID, RequestID: requestID, TenantID: tenantID, UserID: userID, ProjectID: projectID, ProjectVersion: 2, CorrelationID: correlationID,
			Workspace: WorkspaceExportBinding{BindingID: bindingID, Version: 1, Revision: "git:head", ManifestHash: workspaceManifestHash}, Format: "pdf",
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

	// The public application adapter expands only caller-selected revision IDs
	// into the exact trusted manifest and commits its builder input, Run, export,
	// and idempotency response together.
	values := portfolioFixturePayloadStore{inner: &missionPayloadStore{values: map[string][]byte{}}, fixtures: map[string][]byte{"encrypted://brief": []byte(`{"schema_version":1,"brief":"Build the exact production portfolio."}`), "encrypted://reflection": []byte(`{"schema_version":1,"reflection":"The work and evidence are ready for export."}`)}}
	service := PortfolioExportService{Pool: pool, Store: store, Payloads: values, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xc5}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xd5}, 32), BehaviorEnvironment: "production", RunTimeout: 20 * time.Minute, RunMaxSteps: 16, RunMaxCostMicrounits: 200000, RunMaxAttempts: 5, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	metadata := productapi.CommandMetadata{RequestID: "b5000000-0000-4000-8000-000000000030", ClientRequestID: "portfolio-public-client-b5", IdempotencyKey: "portfolio-public-idempotency-b5", TenantID: tenantID, UserID: userID, SessionID: "b5000000-0000-4000-8000-000000000031"}
	publicCommand := productapi.CreatePortfolioExportCommand{CommandMetadata: metadata, ProjectID: projectID, ExpectedProjectVersion: 2, ExpectedWorkspaceBindingVersion: 1, WorkspaceRevision: "git:head", Format: "pdf", ArtifactRevisionIDs: []string{revisionID}}
	publicExport, err := service.Request(ctx, publicCommand)
	if err != nil || publicExport.Status != "requested" || publicExport.Version != 1 || publicExport.RunID == "" || publicExport.RevisionManifestHash == "" || publicExport.Replayed {
		t.Fatalf("public export=%#v err=%v", publicExport, err)
	}
	retryPublic := publicCommand
	retryPublic.RequestID = "b5000000-0000-4000-8000-000000000032"
	retryPublic.SessionID = "b5000000-0000-4000-8000-000000000033"
	replayedPublic, err := service.Request(ctx, retryPublic)
	if err != nil || !replayedPublic.Replayed || replayedPublic.ID != publicExport.ID || replayedPublic.RunID != publicExport.RunID {
		t.Fatalf("public replay=%#v err=%v", replayedPublic, err)
	}
	substitutedPublic := publicCommand
	substitutedPublic.Format = "html"
	if _, err = service.Request(ctx, substitutedPublic); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("public substitution err=%v", err)
	}
	gotPublic, err := service.Get(ctx, tenantID, userID, publicExport.ID)
	if err != nil || gotPublic.ID != publicExport.ID || gotPublic.Status != "requested" || gotPublic.Version != 1 || gotPublic.Format != "pdf" {
		t.Fatalf("public get=%#v err=%v", gotPublic, err)
	}
	var publicConversations, publicRuns, publicMessages, publicStarts, publicIdempotency int
	var profile, snapshotID, messageRef, messageHash string
	err = admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.conversations WHERE tenant_id=$1 AND last_run_id=$2 AND version=2 AND mode='project'),
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=$2),
		(SELECT count(*) FROM agent.run_messages WHERE tenant_id=$1 AND run_id=$2 AND role='user' AND source_kind='conversation_user'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id=$2 AND command_type='StartAgentRun'),
		(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND operation_id='portfolio-exports.request.v2' AND status='completed'),
		r.behavior_profile,r.profile_snapshot_id,m.payload_ref,m.payload_hash
		FROM agent.runs r JOIN agent.run_messages m ON m.tenant_id=r.tenant_id AND m.run_id=r.id AND m.role='user'
		WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, publicExport.RunID).Scan(&publicConversations, &publicRuns, &publicMessages, &publicStarts, &publicIdempotency, &profile, &snapshotID, &messageRef, &messageHash)
	if err != nil || publicConversations != 1 || publicRuns != 1 || publicMessages != 1 || publicStarts != 1 || publicIdempotency != 1 || profile != "artifact_builder" || snapshotID == "" {
		t.Fatalf("public durable conversations=%d runs=%d messages=%d starts=%d idem=%d profile=%s snapshot=%s err=%v", publicConversations, publicRuns, publicMessages, publicStarts, publicIdempotency, profile, snapshotID, err)
	}
	messageBody, err := values.Get(ctx, payload.Descriptor{TenantID: tenantID, Class: "run-message", ContentType: "application/json"}, payload.Manifest{Ref: messageRef, Hash: messageHash})
	if err != nil || !bytes.Contains(messageBody, []byte(`INPUT_MANIFEST=`)) || !bytes.Contains(messageBody, []byte(revisionID)) || !bytes.Contains(messageBody, []byte(`artifact_export`)) {
		t.Fatalf("builder input=%s err=%v", messageBody, err)
	}

	// A Portfolio becomes ready only from the trusted artifact_export receipt.
	// The model's final prose and the Run terminal state are deliberately not
	// part of this decision. Exercise the real Run/Tool effect ledger so the
	// reconciler must bind the exact result event and execution attempt.
	var publicStartCommandID, publicStartRef, publicStartHash string
	if err = admin.QueryRow(ctx, `SELECT command_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND command_type='StartAgentRun'`, tenantID, publicExport.RunID).Scan(&publicStartCommandID, &publicStartRef, &publicStartHash); err != nil {
		t.Fatal(err)
	}
	agentRuns := executionpostgres.RunStore{Pool: agentPool, Appender: appender, IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Tokens: runTokens, LeaseTTL: 2 * time.Minute, Now: func() time.Time { return now }, Behavior: behaviorResolver}
	publicClaim, err := agentRuns.ClaimStart(ctx, executionpostgres.ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: publicStartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: publicExport.RunID, PayloadRef: publicStartRef, PayloadHash: publicStartHash}, ConsumerName: "portfolio-builder", WorkerID: "portfolio-agent-b5", CorrelationID: publicCommand.RequestID, Actor: json.RawMessage(`{"kind":"service"}`), RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/run-started", Hash: "public-run-started"}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/run-attempt-started", Hash: "public-run-attempt-started"}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/run-attempt-expired", Hash: "public-run-attempt-expired"}})
	if err != nil {
		t.Fatalf("claim public portfolio builder: %v", err)
	}
	requestHash := strings.Repeat("a", 64)
	descriptorSnapshotID := "artifact_export@sha256:" + strings.Repeat("b", 64)
	normalizedInput, err := values.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: publicExport.ID, Class: "tool-input", ContentType: "application/json"}, []byte(`{"schema_version":1,"target_kind":"portfolio_export","target_id":"`+publicExport.ID+`","media_type":"application/pdf"}`))
	if err != nil {
		t.Fatal(err)
	}
	executePayload, err := values.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: publicExport.ID, Class: "tool-command", ContentType: "application/json"}, []byte(`{"schema_version":1,"tool_name":"artifact_export"}`))
	if err != nil {
		t.Fatal(err)
	}
	requestedTools, err := agentRuns.RequestTools(ctx, executionpostgres.RequestToolsCommand{Claim: publicClaim, ExpectedRunVersion: publicClaim.RunVersion, StepID: "portfolio-export-write", JoinPolicy: "all", QuorumCount: 1, PlanResultHash: strings.Repeat("c", 64), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: publicCommand.RequestID, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/run-attempt-completed", Hash: "public-run-attempt-completed"}, ToolRequests: []executionpostgres.ToolRequest{{ToolName: "artifact_export", DescriptorSnapshotID: descriptorSnapshotID, NormalizedInputRef: normalizedInput.Ref, RequestHash: requestHash, EffectClass: "reconcilable_write", EffectKey: "portfolio_export:" + publicExport.ID, EffectScope: "tenant:" + tenantID + ":portfolio_export:" + publicExport.ID, ProviderID: "lites-artifact-store", Required: true, QueueClass: "background", ResourceClass: "artifact-export", Priority: 50, CostUnits: 1, MaxAttempts: 5, RequestedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/tool-requested", Hash: "public-tool-requested"}, ExecuteCommand: executionpostgres.PayloadPointer{Ref: executePayload.Ref, Hash: executePayload.Hash}}}})
	if err != nil || len(requestedTools.ToolCalls) != 1 {
		t.Fatalf("request artifact export=%#v err=%v", requestedTools, err)
	}
	requestedTool := requestedTools.ToolCalls[0]
	toolClaim, err := agentRuns.ClaimTool(ctx, executionpostgres.ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: requestedTool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: requestedTool.ToolCallID, PayloadRef: executePayload.Ref, PayloadHash: executePayload.Hash}, ConsumerName: "tool-worker", WorkerID: "artifact-export-worker-b5", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: publicCommand.RequestID, ToolStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/tool-started", Hash: "public-tool-started"}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/tool-attempt-started", Hash: "public-tool-attempt-started"}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/tool-attempt-expired", Hash: "public-tool-attempt-expired"}})
	if err != nil {
		t.Fatalf("claim artifact export: %v", err)
	}
	receipt := portfolioReceipt{SchemaVersion: 1, TargetKind: "portfolio_export", TargetID: publicExport.ID, ObjectRef: "s3://portfolio-artifacts/exports/" + publicExport.ID + ".pdf", ObjectVersion: "version-b5-1", ContentHash: strings.Repeat("d", 64), MediaType: "application/pdf", ByteSize: 8192, ScanResultHash: strings.Repeat("e", 64)}
	receiptBody, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptManifest, err := values.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: toolClaim.ToolCallID, Class: "tool-result", ContentType: "application/json"}, receiptBody)
	if err != nil {
		t.Fatal(err)
	}
	completedEnvelope, err := json.Marshal(map[string]any{"event_type": "tool_call_completed", "data": map[string]any{"schema_version": 1, "tool_call_id": toolClaim.ToolCallID, "run_id": toolClaim.RunID, "attempt_id": toolClaim.AttemptID, "fence": toolClaim.Fence, "tool_name": "artifact_export", "descriptor_snapshot_id": descriptorSnapshotID, "request_hash": requestHash, "effect_class": "reconcilable_write", "provider_request_id": toolClaim.ProviderRequestID, "result_ref": receiptManifest.Ref, "result_payload_hash": receiptManifest.Hash, "result_hash": receiptManifest.Hash, "target_state": "succeeded", "external_resource_ref": receipt.ObjectRef, "effect_disposition": "confirmed", "policy_snapshot_id": "policy-b5", "policy_snapshot_hash": strings.Repeat("f", 64), "overlay_version": 1}})
	if err != nil {
		t.Fatal(err)
	}
	completedManifest, err := values.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: toolClaim.AttemptID + ":tool-completed", Class: "event-payload", ContentType: "application/json"}, completedEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	toolCompletion, err := agentRuns.CompleteEffectTool(ctx, executionpostgres.CompleteToolCommand{Claim: toolClaim, ExpectedToolVersion: toolClaim.ToolCallVersion, TargetState: statemachine.ToolCallSucceeded, ResultHash: receiptManifest.Hash, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: publicCommand.RequestID, ToolCompletedEvent: executionpostgres.PayloadPointer{Ref: completedManifest.Ref, Hash: completedManifest.Hash}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/tool-attempt-completed", Hash: "public-tool-attempt-completed"}, GroupJoinedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/group-joined", Hash: "public-group-joined"}, RunResumeQueuedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://public/run-resume-queued", Hash: "public-run-resume-queued"}, ResumeCommand: executionpostgres.PayloadPointer{Ref: "encrypted://public/run-resume", Hash: "public-run-resume"}, ResumeQueueClass: "background", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}, executionpostgres.EffectCompletion{ExternalResourceRef: receipt.ObjectRef})
	if err != nil || toolCompletion.Status != statemachine.ToolCallSucceeded {
		t.Fatalf("complete artifact export=%#v err=%v", toolCompletion, err)
	}
	portfolioReconciler := PortfolioExportReconciler{Pool: pool, Store: store, Appender: appender, Payloads: values, IDKey: key, StoreEpoch: epoch, Retention: 30 * 24 * time.Hour, Now: func() time.Time { return now }}
	finalizationTenants, err := portfolioReconciler.ListTenantIDs(ctx, "", 100)
	if err != nil || len(finalizationTenants) != 1 || finalizationTenants[0] != tenantID {
		t.Fatalf("portfolio finalization tenants=%v err=%v", finalizationTenants, err)
	}
	finalized, err := portfolioReconciler.ReconcileTenant(ctx, tenantID, 100)
	if err != nil || finalized.Scanned != 1 || finalized.Ready != 1 || finalized.Failed != 0 || finalized.Replayed != 0 {
		t.Fatalf("portfolio finalization=%#v err=%v", finalized, err)
	}
	gotReady, err := service.Get(ctx, tenantID, userID, publicExport.ID)
	if err != nil || gotReady.Status != "ready" || gotReady.Version != 3 || gotReady.ContentHash == nil || *gotReady.ContentHash != receipt.ContentHash || gotReady.MediaType == nil || *gotReady.MediaType != receipt.MediaType || gotReady.ByteSize == nil || *gotReady.ByteSize != receipt.ByteSize || gotReady.CompletedAt == nil || gotReady.ExpiresAt == nil || !gotReady.ExpiresAt.Equal(gotReady.CompletedAt.Add(30*24*time.Hour)) {
		t.Fatalf("ready public export=%#v err=%v", gotReady, err)
	}
	var finalizationVersions, finalObjectRef, finalObjectVersion, finalScanHash string
	if err = admin.QueryRow(ctx, `SELECT (SELECT array_agg(aggregate_version ORDER BY aggregate_version)::text FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='portfolio_export' AND aggregate_id=$2),object_ref,object_version,scan_result_hash FROM product.portfolio_exports WHERE tenant_id=$1 AND id=$2`, tenantID, publicExport.ID).Scan(&finalizationVersions, &finalObjectRef, &finalObjectVersion, &finalScanHash); err != nil || finalizationVersions != "{1,2,3}" || finalObjectRef != receipt.ObjectRef || finalObjectVersion != receipt.ObjectVersion || finalScanHash != receipt.ScanResultHash {
		t.Fatalf("finalization versions=%s object=%s/%s scan=%s err=%v", finalizationVersions, finalObjectRef, finalObjectVersion, finalScanHash, err)
	}

	failedPublicCommand := publicCommand
	failedPublicCommand.RequestID = "b5000000-0000-4000-8000-000000000034"
	failedPublicCommand.ClientRequestID = "portfolio-public-client-failed-b5"
	failedPublicCommand.IdempotencyKey = "portfolio-public-idempotency-failed-b5"
	failedPublicCommand.SessionID = "b5000000-0000-4000-8000-000000000035"
	failedPublicExport, err := service.Request(ctx, failedPublicCommand)
	if err != nil || failedPublicExport.Status != "requested" || failedPublicExport.Version != 1 {
		t.Fatalf("failed-path public request=%#v err=%v", failedPublicExport, err)
	}
	var failedStartCommandID, failedStartRef, failedStartHash string
	if err = admin.QueryRow(ctx, `SELECT command_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND command_type='StartAgentRun'`, tenantID, failedPublicExport.RunID).Scan(&failedStartCommandID, &failedStartRef, &failedStartHash); err != nil {
		t.Fatal(err)
	}
	failedPublicClaim, err := agentRuns.ClaimStart(ctx, executionpostgres.ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: epoch, CommandID: failedStartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: failedPublicExport.RunID, PayloadRef: failedStartRef, PayloadHash: failedStartHash}, ConsumerName: "portfolio-builder", WorkerID: "portfolio-agent-failed-b5", CorrelationID: failedPublicCommand.RequestID, Actor: json.RawMessage(`{"kind":"service"}`), RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://failed-public/run-started", Hash: "failed-public-run-started"}, AttemptStartedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://failed-public/run-attempt-started", Hash: "failed-public-run-attempt-started"}, AttemptExpiredEvent: executionpostgres.PayloadPointer{Ref: "encrypted://failed-public/run-attempt-expired", Hash: "failed-public-run-attempt-expired"}})
	if err != nil {
		t.Fatalf("claim failed-path portfolio builder: %v", err)
	}
	failedRun, err := agentRuns.CompleteRunTerminal(ctx, executionpostgres.CompleteRunCommand{Claim: failedPublicClaim, ExpectedRunVersion: failedPublicClaim.RunVersion, TargetState: statemachine.RunFailed, ResultHash: strings.Repeat("1", 64), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: failedPublicCommand.RequestID, RunEvent: executionpostgres.PayloadPointer{Ref: "encrypted://failed-public/run-failed", Hash: "failed-public-run-failed"}, AttemptCompletedEvent: executionpostgres.PayloadPointer{Ref: "encrypted://failed-public/attempt-completed", Hash: "failed-public-attempt-completed"}})
	if err != nil || failedRun.Status != statemachine.RunFailed {
		t.Fatalf("terminal failed builder run=%#v err=%v", failedRun, err)
	}
	failedFinalized, err := portfolioReconciler.ReconcileTenant(ctx, tenantID, 100)
	if err != nil || failedFinalized.Scanned != 1 || failedFinalized.Ready != 0 || failedFinalized.Failed != 1 {
		t.Fatalf("failed portfolio finalization=%#v err=%v", failedFinalized, err)
	}
	gotFailed, err := service.Get(ctx, tenantID, userID, failedPublicExport.ID)
	if err != nil || gotFailed.Status != "failed" || gotFailed.Version != 2 || gotFailed.FailureCode == nil || *gotFailed.FailureCode != "artifact_builder_run_failed" || gotFailed.StartedAt == nil || gotFailed.CompletedAt != nil || gotFailed.ExpiresAt != nil {
		t.Fatalf("failed public export=%#v err=%v", gotFailed, err)
	}
	var failedFinalizationVersions string
	if err = admin.QueryRow(ctx, `SELECT array_agg(aggregate_version ORDER BY aggregate_version)::text FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='portfolio_export' AND aggregate_id=$2`, tenantID, failedPublicExport.ID).Scan(&failedFinalizationVersions); err != nil || failedFinalizationVersions != "{1,2}" {
		t.Fatalf("failed finalization versions=%s err=%v", failedFinalizationVersions, err)
	}
}
