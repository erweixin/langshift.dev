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

	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/integrationfixture"
	"github.com/langshift/lites/internal/payload"
	productapi "github.com/langshift/lites/internal/product/api"
)

func TestRouteServiceFreezesInputsAndCommitsReplaySafeGenerateAndAccept(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 20, 0, 0, 0, time.UTC)
	userID := "a7000000-0000-4000-8000-000000000001"
	tenantID := "a7000000-0000-4000-8000-000000000002"
	roleID := "a7000000-0000-4000-8000-000000000003"
	missionID := "a7000000-0000-4000-8000-000000000004"
	capabilityID := "a7000000-0000-4000-8000-000000000005"
	claimID := "a7000000-0000-4000-8000-000000000006"
	evidenceID := "a7000000-0000-4000-8000-000000000007"
	epoch := "a7000000-0000-4000-8000-000000000008"
	claimHash := strings.Repeat("7", 64)
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
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'route-service-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Route Service Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'route-service-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.capabilities(id,tenant_id,slug,revision,status,spec,evidence_guidance) VALUES($1,$2,'route-service-capability',1,'active','{}','{}')`, []any{capabilityID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,0,$5)`, []any{missionID, tenantID, userID, roleID, claimHash}},
		{`INSERT INTO product.mission_focuses(tenant_id,user_id,mission_id,focus_version) VALUES($1,$2,$3,1)`, []any{tenantID, userID, missionID}},
		{`INSERT INTO product.capability_claims(id,tenant_id,user_id,claim_identity_id,claim_revision,mission_id,capability_id,status,origin,verification_level,statement_ref,recorded_at) VALUES($1,$2,$3,$1,1,$4,$5,'active','user_asserted','user_confirmed','encrypted://claim',$6)`, []any{claimID, tenantID, userID, missionID, capabilityID, now}},
		{`INSERT INTO product.evidence(id,tenant_id,user_id,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,$4,'assessment','verified','user','encrypted://evidence',$5,$6)`, []any{evidenceID, tenantID, userID, missionID, strings.Repeat("e", 64), now}},
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
	key := bytes.Repeat([]byte{0xa7}, 32)
	store := RouteStore{Pool: pool, Appender: eventAppender(now), IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	service := RouteService{Pool: pool, Store: store, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xa8}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xa9}, 32), CursorKey: bytes.Repeat([]byte{0xaa}, 32), IdempotencyTTL: 24 * time.Hour, BehaviorProfile: string(behavior.RoutePlanner), BehaviorEnvironment: "production", OntologySnapshotID: "ontology:1.0.0:" + strings.Repeat("b", 64), ContentSnapshotID: "content:1.0.0:" + strings.Repeat("c", 64), Now: func() time.Time { return now }}
	metadata := productapi.CommandMetadata{RequestID: "a7000000-0000-4000-8000-000000000010", ClientRequestID: "route-generate-request-a7", IdempotencyKey: "route-generate-idempotency-a7", TenantID: tenantID, UserID: userID, SessionID: "a7000000-0000-4000-8000-000000000009"}
	generate := productapi.GenerateRouteCommand{CommandMetadata: metadata, MissionID: missionID, ExpectedRouteVersion: 0, ExpectedClaimSetHash: claimHash}
	generated, err := service.Generate(ctx, generate)
	if err != nil || generated.Replayed || generated.Status != "generating" || generated.RevisionVersion != 1 {
		t.Fatalf("generated=%#v err=%v", generated, err)
	}
	replayedGeneration, err := service.Generate(ctx, generate)
	if err != nil || !replayedGeneration.Replayed || replayedGeneration.RouteRevisionID != generated.RouteRevisionID {
		t.Fatalf("generation replay=%#v err=%v", replayedGeneration, err)
	}
	substitution := generate
	substitution.ExpectedRouteVersion = 1
	if _, err = service.Generate(ctx, substitution); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("generation substitution err=%v", err)
	}
	var inputManifest json.RawMessage
	if err = admin.QueryRow(ctx, `SELECT input_manifest FROM product.route_revisions WHERE id=$1`, generated.RouteRevisionID).Scan(&inputManifest); err != nil {
		t.Fatal(err)
	}
	var frozen routeInputManifest
	if err = json.Unmarshal(inputManifest, &frozen); err != nil || frozen.SchemaVersion != 2 || len(frozen.ClaimRevisions) != 1 || len(frozen.EvidenceRevisions) != 1 || frozen.AgentProfileSnapshotID != binding.SnapshotID || frozen.AgentProfile.SnapshotID != binding.SnapshotID || frozen.AgentProfile.ChannelID != binding.ChannelID || frozen.AgentProfile.Sequence != binding.Sequence || frozen.AgentProfile.Profile != string(behavior.RoutePlanner) || frozen.AgentProfile.Environment != "production" || frozen.Mission.ClaimSetHash != claimHash {
		t.Fatalf("frozen=%#v err=%v", frozen, err)
	}
	routeBody := json.RawMessage(`{"schema_version":1,"stages":[{"id":"foundation"}]}`)
	routeManifest, err := payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: generated.RouteRevisionID, Class: routePayloadClass, ContentType: "application/json"}, routeBody)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteGeneration(ctx, CompleteRouteGenerationCommand{MutationID: "route-complete-a7", RevisionID: generated.RouteRevisionID, TenantID: tenantID, UserID: userID, ExpectedRevisionVersion: 1, RoutePayload: PayloadPointer{Ref: routeManifest.Ref, Hash: routeManifest.Hash}, CompletedEvent: PayloadPointer{Ref: "encrypted://route-completed", Hash: strings.Repeat("f", 64)}, CorrelationID: metadata.RequestID, Actor: missionActor(userID, metadata.SessionID)})
	if err != nil || completed.Status != "proposed" || completed.RevisionVersion != 2 {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	listed, err := service.List(ctx, productapi.RouteListQuery{TenantID: tenantID, UserID: userID, MissionID: missionID})
	if err != nil || len(listed.Items) != 1 || !bytes.Equal(listed.Items[0].Route, routeBody) {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	acceptMetadata := metadata
	acceptMetadata.ClientRequestID = "route-accept-request-a7"
	acceptMetadata.IdempotencyKey = "route-accept-idempotency-a7"
	accept := productapi.AcceptRouteCommand{CommandMetadata: acceptMetadata, RouteRevisionID: generated.RouteRevisionID, ExpectedRevisionVersion: 2, ExpectedRouteVersion: 0, ExpectedClaimSetHash: claimHash}
	accepted, err := service.Accept(ctx, accept)
	if err != nil || accepted.Replayed || accepted.Status != "accepted" || accepted.RouteVersion != 1 || accepted.CurrentRouteRevisionID != generated.RouteRevisionID {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	replayedAcceptance, err := service.Accept(ctx, accept)
	if err != nil || !replayedAcceptance.Replayed || replayedAcceptance.EventID != accepted.EventID {
		t.Fatalf("accept replay=%#v err=%v", replayedAcceptance, err)
	}
	var idempotencyRows, planningCommands, dailyCommands int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND status='completed'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='RoutePlanningRequested'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='GenerateDailyTask')`, tenantID).Scan(&idempotencyRows, &planningCommands, &dailyCommands); err != nil || idempotencyRows != 2 || planningCommands != 1 || dailyCommands != 1 {
		t.Fatalf("idempotency=%d planning=%d daily=%d err=%v", idempotencyRows, planningCommands, dailyCommands, err)
	}
}
