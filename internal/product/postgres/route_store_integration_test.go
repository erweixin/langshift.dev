//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/product/route"
)

func TestRouteStoreMaterializesStaleAndAtomicallyAcceptsAndSupersedes(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	userID := "a6000000-0000-4000-8000-000000000001"
	tenantID := "a6000000-0000-4000-8000-000000000002"
	roleID := "a6000000-0000-4000-8000-000000000003"
	missionID := "a6000000-0000-4000-8000-000000000004"
	epoch := "a6000000-0000-4000-8000-000000000005"
	if _, err := admin.Exec(ctx, `DELETE FROM identity.tenants WHERE id=$1`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM identity.users WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'route-store-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Route Store Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'route-store-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,0,'claims-a6')`, []any{missionID, tenantID, userID, roleID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	store := RouteStore{Pool: pool, Appender: eventAppender(now), IDKey: bytes.Repeat([]byte{0xa6}, 32), StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	actor := json.RawMessage(`{"kind":"user","user_id":"a6000000-0000-4000-8000-000000000001"}`)
	eventPointer := PayloadPointer{Ref: "encrypted://route-event", Hash: strings.Repeat("a", 64)}
	staleRevision := "a6000000-0000-4000-8000-000000000010"
	_, err := store.BeginGeneration(ctx, BeginRouteGenerationCommand{MutationID: "begin-stale", RevisionID: staleRevision, TenantID: tenantID, UserID: userID, MissionID: missionID, ExpectedRouteVersion: 0, ExpectedClaimSetHash: "claims-a6", PlannerCommandID: "a6000000-0000-4000-8000-000000000011", InputManifest: json.RawMessage(`{"claim_revisions":[],"evidence_revisions":[]}`), AgentProfileSnapshotID: "route_planner@1", OntologySnapshotID: "ontology@1", ContentSnapshotID: "content@1", CorrelationID: "a6000000-0000-4000-8000-000000000012", Actor: actor, RequestedEvent: eventPointer})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE product.missions SET route_version=1,claim_set_hash='claims-b6',version=version+1 WHERE id=$1`, missionID); err != nil {
		t.Fatal(err)
	}
	stale, err := store.CompleteGeneration(ctx, CompleteRouteGenerationCommand{MutationID: "complete-stale", RevisionID: staleRevision, TenantID: tenantID, UserID: userID, ExpectedRevisionVersion: 1, RoutePayload: PayloadPointer{Ref: "encrypted://route-stale", Hash: strings.Repeat("b", 64)}, CompletedEvent: eventPointer, CorrelationID: "a6000000-0000-4000-8000-000000000013", Actor: actor})
	if !errors.Is(err, ErrRouteResultStale) || !stale.Stale || stale.Status != string(route.Stale) {
		t.Fatalf("stale=%#v err=%v", stale, err)
	}
	acceptedOne := prepareAndAcceptRoute(t, ctx, store, tenantID, userID, missionID, "a6000000-0000-4000-8000-000000000020", "a6000000-0000-4000-8000-000000000021", 1, "claims-b6", actor, eventPointer)
	if acceptedOne.Status != string(route.Accepted) || acceptedOne.RouteVersion != 2 || acceptedOne.CurrentRouteRevisionID != acceptedOne.RevisionID {
		t.Fatalf("accepted one=%#v", acceptedOne)
	}
	acceptedTwo := prepareAndAcceptRoute(t, ctx, store, tenantID, userID, missionID, "a6000000-0000-4000-8000-000000000030", "a6000000-0000-4000-8000-000000000031", 2, "claims-b6", actor, eventPointer)
	if acceptedTwo.RouteVersion != 3 || acceptedTwo.CurrentRouteRevisionID != acceptedTwo.RevisionID {
		t.Fatalf("accepted two=%#v", acceptedTwo)
	}
	var staleStatus, previousStatus, currentStatus, currentID string
	var routeVersion uint64
	var dailyCommands int
	err = admin.QueryRow(ctx, `SELECT (SELECT status FROM product.route_revisions WHERE id=$1),(SELECT status FROM product.route_revisions WHERE id=$2),(SELECT status FROM product.route_revisions WHERE id=$3),m.current_route_revision_id::text,m.route_version,(SELECT count(*) FROM agent.outbox WHERE tenant_id=$4 AND command_type='GenerateDailyTask') FROM product.missions m WHERE m.id=$5`, staleRevision, acceptedOne.RevisionID, acceptedTwo.RevisionID, tenantID, missionID).Scan(&staleStatus, &previousStatus, &currentStatus, &currentID, &routeVersion, &dailyCommands)
	if err != nil || staleStatus != "stale" || previousStatus != "superseded" || currentStatus != "accepted" || currentID != acceptedTwo.RevisionID || routeVersion != 3 || dailyCommands != 0 {
		t.Fatalf("stale=%s previous=%s current=%s currentID=%s routeVersion=%d commands=%d err=%v", staleStatus, previousStatus, currentStatus, currentID, routeVersion, dailyCommands, err)
	}
}

func prepareAndAcceptRoute(t *testing.T, ctx context.Context, store RouteStore, tenantID, userID, missionID, revisionID, plannerCommandID string, expectedRouteVersion uint64, claimHash string, actor json.RawMessage, eventPointer PayloadPointer) RouteResult {
	t.Helper()
	begin, err := store.BeginGeneration(ctx, BeginRouteGenerationCommand{MutationID: "begin-" + revisionID, RevisionID: revisionID, TenantID: tenantID, UserID: userID, MissionID: missionID, ExpectedRouteVersion: expectedRouteVersion, ExpectedClaimSetHash: claimHash, PlannerCommandID: plannerCommandID, InputManifest: json.RawMessage(`{"claim_revisions":[],"evidence_revisions":[]}`), AgentProfileSnapshotID: "route_planner@1", OntologySnapshotID: "ontology@1", ContentSnapshotID: "content@1", CorrelationID: "a6000000-0000-4000-8000-000000000040", Actor: actor, RequestedEvent: eventPointer})
	if err != nil || begin.RevisionVersion != 1 {
		t.Fatalf("begin=%#v err=%v", begin, err)
	}
	completed, err := store.CompleteGeneration(ctx, CompleteRouteGenerationCommand{MutationID: "complete-" + revisionID, RevisionID: revisionID, TenantID: tenantID, UserID: userID, ExpectedRevisionVersion: 1, RoutePayload: PayloadPointer{Ref: "encrypted://route/" + revisionID, Hash: strings.Repeat("c", 64)}, CompletedEvent: eventPointer, CorrelationID: "a6000000-0000-4000-8000-000000000041", Actor: actor})
	if err != nil || completed.Status != string(route.Proposed) || completed.RevisionVersion != 2 {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	accepted, err := store.Accept(ctx, AcceptRouteCommand{MutationID: "accept-" + revisionID, RevisionID: revisionID, TenantID: tenantID, UserID: userID, ExpectedRevisionVersion: 2, ExpectedRouteVersion: expectedRouteVersion, ExpectedClaimSetHash: claimHash, CorrelationID: "a6000000-0000-4000-8000-000000000042", Actor: actor, AcceptedEvent: eventPointer})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
