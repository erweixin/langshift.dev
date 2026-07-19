//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	productapi "github.com/langshift/lites/internal/product/api"
)

type capabilityProjectorStub struct{}

func (capabilityProjectorStub) EnsureCapabilities(context.Context, pgx.Tx, string, ...string) error {
	return nil
}

func TestCapabilityClaimServiceAppendsRevisionAndInvalidatesRoutes(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC)
	userID := "cc000000-0000-4000-8000-000000000001"
	tenantID := "cc000000-0000-4000-8000-000000000002"
	roleID := "cc000000-0000-4000-8000-000000000003"
	missionID := "cc000000-0000-4000-8000-000000000004"
	capabilityID := "cc000000-0000-4000-8000-000000000005"
	routeID := "cc000000-0000-4000-8000-000000000006"
	epoch := "cc000000-0000-4000-8000-000000000007"
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
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'capability-claim-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Claim Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'claim-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.capabilities(id,tenant_id,slug,revision,status,spec,evidence_guidance) VALUES($1,$2,'claim-capability',1,'active','{}','{}')`, []any{capabilityID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,$5)`, []any{missionID, tenantID, userID, roleID, strings.Repeat("0", 64)}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,version,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,2,$4,1,'accepted',$5,0,'{}','encrypted://route','route@claim','ontology@claim','content@claim',$6)`, []any{routeID, tenantID, userID, missionID, strings.Repeat("0", 64), now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE id=$2`, []any{routeID, missionID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	key := bytes.Repeat([]byte{0xcc}, 32)
	service := CapabilityClaimService{Pool: pool, Appender: eventAppender(now), Payloads: payloads, Catalog: capabilityProjectorStub{}, IDKey: key, CursorKey: bytes.Repeat([]byte{0xcd}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xce}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xcf}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	metadata := productapi.CommandMetadata{RequestID: "cc000000-0000-4000-8000-000000000008", ClientRequestID: "claim-create-request-cc", IdempotencyKey: "claim-create-idempotency-cc", TenantID: tenantID, UserID: userID, SessionID: "cc000000-0000-4000-8000-000000000009"}
	created, err := service.Create(ctx, productapi.CreateCapabilityClaimCommand{CommandMetadata: metadata, MissionID: missionID, CapabilityID: capabilityID, Origin: "user_asserted", Statement: "I led the migration.", EvidenceIDs: []string{}})
	if err != nil || created.Replayed || created.Version != 1 || len(created.ClaimSetHash) != 64 {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	replayed, err := service.Create(ctx, productapi.CreateCapabilityClaimCommand{CommandMetadata: metadata, MissionID: missionID, CapabilityID: capabilityID, Origin: "user_asserted", Statement: "I led the migration.", EvidenceIDs: []string{}})
	if err != nil || !replayed.Replayed || replayed.ID != created.ID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	var routeStatus, staleReason, missionHash string
	var currentRouteID *string
	if err = admin.QueryRow(ctx, `SELECT r.status,COALESCE(r.stale_reason,''),m.claim_set_hash,m.current_route_revision_id::text FROM product.route_revisions r JOIN product.missions m ON m.id=r.mission_id WHERE r.id=$1`, routeID).Scan(&routeStatus, &staleReason, &missionHash, &currentRouteID); err != nil || routeStatus != "stale" || staleReason != "claim_set_changed" || missionHash != created.ClaimSetHash || currentRouteID != nil {
		t.Fatalf("route=%s reason=%s hash=%s current=%v err=%v", routeStatus, staleReason, missionHash, currentRouteID, err)
	}
	reviseMetadata := metadata
	reviseMetadata.ClientRequestID = "claim-correct-request-cc"
	reviseMetadata.IdempotencyKey = "claim-correct-idempotency-cc"
	corrected, err := service.Revise(ctx, productapi.ReviseCapabilityClaimCommand{CommandMetadata: reviseMetadata, ClaimIdentityID: created.ID, Action: "correct", Reason: "The first statement overstated my ownership.", CapabilityID: capabilityID, Statement: "I supported the migration.", EvidenceIDs: []string{}, ExpectedVersion: 1})
	if err != nil || corrected.Version != 2 || corrected.ClaimSetHash == created.ClaimSetHash {
		t.Fatalf("corrected=%#v err=%v", corrected, err)
	}
	conflictMetadata := metadata
	conflictMetadata.ClientRequestID = "claim-stale-request-cc"
	conflictMetadata.IdempotencyKey = "claim-stale-idempotency-cc"
	_, err = service.Revise(ctx, productapi.ReviseCapabilityClaimCommand{CommandMetadata: conflictMetadata, ClaimIdentityID: created.ID, Action: "confirm", Reason: "Confirm stale revision.", EvidenceIDs: []string{}, ExpectedVersion: 1})
	if !errors.Is(err, productapi.ErrStateConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
	listed, err := service.List(ctx, productapi.CapabilityClaimListQuery{TenantID: tenantID, UserID: userID})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Version != 2 || listed.Items[0].Statement != "I supported the migration." || listed.Items[0].Reason == nil {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	var claimRows, claimEvents, staleEvents int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.capability_claims WHERE tenant_id=$1),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type='CapabilityClaimRevised'),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type='RouteMarkedStale')`, tenantID).Scan(&claimRows, &claimEvents, &staleEvents); err != nil || claimRows != 2 || claimEvents != 2 || staleEvents != 1 {
		t.Fatalf("rows=%d claim_events=%d stale_events=%d err=%v", claimRows, claimEvents, staleEvents, err)
	}
}
