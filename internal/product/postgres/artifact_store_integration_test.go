//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type artifactEpochStub struct{ epoch string }

func (stub artifactEpochStub) CurrentStoreEpoch(context.Context) (string, error) {
	return stub.epoch, nil
}

func artifactPool(t *testing.T, ctx context.Context, environment string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(environment)
	if value == "" {
		t.Skip(environment + " is not configured")
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestArtifactRevisionsAreScannedEvidenceBoundReplaySafeAndCASProtected(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	userID := "a4000000-0000-4000-8000-000000000001"
	tenantID := "a4000000-0000-4000-8000-000000000002"
	roleID := "a4000000-0000-4000-8000-000000000003"
	missionID := "a4000000-0000-4000-8000-000000000004"
	routeID := "a4000000-0000-4000-8000-000000000005"
	projectID := "a4000000-0000-4000-8000-000000000006"
	evidenceID := "a4000000-0000-4000-8000-000000000007"
	artifactID := "a4000000-0000-4000-8000-000000000008"
	epoch := "a4000000-0000-4000-8000-000000000009"
	projectEventID := "a4000000-0000-4000-8000-000000000013"
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'artifact-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Artifact Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'artifact-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'claims-a4')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','claims-a4',0,'{}','encrypted://route','route@a4','ontology@a4','content@a4',$5)`, []any{routeID, tenantID, userID, missionID, now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE id=$2`, []any{routeID, missionID}},
		{`INSERT INTO product.evidence(id,tenant_id,user_id,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,$4,'project','verified','project','encrypted://evidence','evidence-hash-a4',$5)`, []any{evidenceID, tenantID, userID, missionID, now}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	seedActiveProject(t, ctx, admin, activeProjectFixture{ProjectID: projectID, TenantID: tenantID, UserID: userID, MissionID: missionID, RouteID: routeID, ProjectEventID: projectEventID, StoreEpoch: epoch, CorrelationID: routeID, At: now})
	clock := now
	store := ArtifactStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }}, IDKey: bytes.Repeat([]byte{0xa4}, 32), StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return clock }}
	create := CreateArtifactCommand{ArtifactID: artifactID, TenantID: tenantID, UserID: userID, ProjectID: projectID, ArtifactKind: "writing", Title: "Production architecture", CorrelationID: routeID, Actor: json.RawMessage(`{"kind":"user"}`), CreatedEvent: PayloadPointer{Ref: "encrypted://artifact-created", Hash: "artifact-created-a4"}}
	created, err := store.CreateArtifact(ctx, create)
	if err != nil || created.Version != 1 || created.Status != "draft" || created.Replayed {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	replayed, err := store.CreateArtifact(ctx, create)
	if err != nil || !replayed.Replayed || replayed.Version != 1 {
		t.Fatalf("create replay=%#v err=%v", replayed, err)
	}
	conflictingCreate := create
	conflictingCreate.Title = "Substituted"
	if _, err = store.CreateArtifact(ctx, conflictingCreate); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("create conflict=%v", err)
	}
	binding := EvidenceBinding{EvidenceID: evidenceID, Version: 1, ContentHash: "evidence-hash-a4"}
	revise := AppendArtifactRevisionCommand{ArtifactID: artifactID, RevisionID: "a4000000-0000-4000-8000-000000000010", TenantID: tenantID, UserID: userID, ExpectedArtifactVersion: 1, ContentHash: "artifact-content-a4-v1", ObjectRef: "s3://artifacts/a4", ObjectVersion: "s3-version-1", WorkspaceRevision: "git:a4-v1", MediaType: "text/markdown", ByteSize: 2048, ScanResultHash: "scan-passed-a4-v1", Evidence: []EvidenceBinding{binding}, CorrelationID: routeID, Actor: json.RawMessage(`{"kind":"service"}`), RevisionCreatedEvent: PayloadPointer{Ref: "encrypted://artifact-revision-1", Hash: "artifact-revision-a4-v1"}}
	first, err := store.AppendArtifactRevision(ctx, revise)
	if err != nil || first.ArtifactVersion != 2 || first.Revision != 1 || first.EvidenceManifestHash == "" || first.Replayed {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	revisionReplay, err := store.AppendArtifactRevision(ctx, revise)
	if err != nil || !revisionReplay.Replayed || revisionReplay.EvidenceManifestHash != first.EvidenceManifestHash {
		t.Fatalf("revision replay=%#v err=%v", revisionReplay, err)
	}
	substitution := revise
	substitution.ScanResultHash = "different-scan"
	if _, err = store.AppendArtifactRevision(ctx, substitution); !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("revision substitution error=%v", err)
	}
	clock = now.Add(time.Second)
	commands := []AppendArtifactRevisionCommand{
		{ArtifactID: artifactID, RevisionID: "a4000000-0000-4000-8000-000000000011", TenantID: tenantID, UserID: userID, ExpectedArtifactVersion: 2, ContentHash: "artifact-content-a4-v2a", ObjectRef: "s3://artifacts/a4-2a", ObjectVersion: "s3-version-2a", WorkspaceRevision: "git:a4-v2a", MediaType: "text/markdown", ByteSize: 3072, ScanResultHash: "scan-passed-a4-v2a", Evidence: []EvidenceBinding{binding}, CorrelationID: routeID, Actor: json.RawMessage(`{"kind":"service"}`), RevisionCreatedEvent: PayloadPointer{Ref: "encrypted://artifact-revision-2a", Hash: "artifact-revision-a4-v2a"}},
		{ArtifactID: artifactID, RevisionID: "a4000000-0000-4000-8000-000000000012", TenantID: tenantID, UserID: userID, ExpectedArtifactVersion: 2, ContentHash: "artifact-content-a4-v2b", ObjectRef: "s3://artifacts/a4-2b", ObjectVersion: "s3-version-2b", WorkspaceRevision: "git:a4-v2b", MediaType: "text/markdown", ByteSize: 4096, ScanResultHash: "scan-passed-a4-v2b", Evidence: []EvidenceBinding{binding}, CorrelationID: routeID, Actor: json.RawMessage(`{"kind":"service"}`), RevisionCreatedEvent: PayloadPointer{Ref: "encrypted://artifact-revision-2b", Hash: "artifact-revision-a4-v2b"}},
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, command := range commands {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, callErr := store.AppendArtifactRevision(ctx, command)
			results <- callErr
		}()
	}
	wait.Wait()
	close(results)
	winners, conflicts := 0, 0
	for callErr := range results {
		if callErr == nil {
			winners++
		} else if errors.Is(callErr, ErrArtifactVersion) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent result: %v", callErr)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent winners=%d conflicts=%d", winners, conflicts)
	}
	var status, currentHash string
	var version uint64
	var currentRevision, revisions, links, events int
	err = admin.QueryRow(ctx, `SELECT status,version,current_revision,current_content_hash,(SELECT count(*) FROM product.artifact_revisions WHERE tenant_id=$1 AND artifact_id=$2),(SELECT count(*) FROM product.artifact_revision_evidence e JOIN product.artifact_revisions r ON r.tenant_id=e.tenant_id AND r.id=e.artifact_revision_id WHERE r.tenant_id=$1 AND r.artifact_id=$2),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='artifact' AND aggregate_id=$2) FROM product.artifacts WHERE tenant_id=$1 AND id=$2`, tenantID, artifactID).Scan(&status, &version, &currentRevision, &currentHash, &revisions, &links, &events)
	if err != nil || status != "ready" || version != 3 || currentRevision != 2 || currentHash == "" || revisions != 2 || links != 2 || events != 3 {
		t.Fatalf("artifact=%s/v%d revision=%d hash=%s rows=%d/%d events=%d err=%v", status, version, currentRevision, currentHash, revisions, links, events, err)
	}
}
