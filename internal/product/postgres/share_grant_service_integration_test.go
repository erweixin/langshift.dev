//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	productapi "github.com/langshift/lites/internal/product/api"
)

func TestShareGrantExactScopeImmediateRevocationAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	ownerID := "f7000000-0000-4000-8000-000000000001"
	granteeID := "f7000000-0000-4000-8000-000000000002"
	otherID := "f7000000-0000-4000-8000-000000000003"
	tenantID := "f7000000-0000-4000-8000-000000000004"
	otherTenantID := "f7000000-0000-4000-8000-000000000005"
	roleID := "f7000000-0000-4000-8000-000000000006"
	missionID := "f7000000-0000-4000-8000-000000000007"
	routeID := "f7000000-0000-4000-8000-000000000008"
	projectID := "f7000000-0000-4000-8000-000000000009"
	epoch := "f7000000-0000-4000-8000-000000000010"
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'share-owner@example.invalid','en','active'),($2,'share-reviewer@example.invalid','en','active'),($3,'share-other@example.invalid','en','active')`, []any{ownerID, granteeID, otherID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'enterprise','Share Tenant','active','US'),($2,'enterprise','Other Share Tenant','active','US')`, []any{tenantID, otherTenantID}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$9),($4,$2,$5,'reviewer','active',$9),($6,$7,$8,'reviewer','active',$9)`, []any{"f7000000-0000-4000-8000-000000000011", tenantID, ownerID, "f7000000-0000-4000-8000-000000000012", granteeID, "f7000000-0000-4000-8000-000000000013", otherTenantID, otherID, now}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'share-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'claims-f7')`, []any{missionID, tenantID, ownerID, roleID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','claims-f7',0,'{}','encrypted://route-f7','route@f7','ontology@f7','content@f7',$5)`, []any{routeID, tenantID, ownerID, missionID, now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE id=$2`, []any{routeID, missionID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	seedActiveProject(t, ctx, admin, activeProjectFixture{ProjectID: projectID, TenantID: tenantID, UserID: ownerID, MissionID: missionID, RouteID: routeID, ProjectEventID: "f7000000-0000-4000-8000-000000000014", StoreEpoch: epoch, CorrelationID: routeID, At: now})
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	service := ShareGrantService{Pool: pool, Appender: eventAppender(now), Payloads: payloads, IDKey: bytes.Repeat([]byte{0xf7}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xf8}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xf9}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	read := productapi.ShareGrantAccessQuery{TenantID: tenantID, GranteeUserID: granteeID, ResourceKind: "project", ResourceID: projectID, ResourceRevision: "1", Scope: "read"}
	if allowed, err := service.Authorize(ctx, read); err != nil || allowed {
		t.Fatalf("before grant allowed=%v err=%v", allowed, err)
	}
	command := productapi.CreateShareGrantCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "f7000000-0000-4000-8000-000000000015", ClientRequestID: "share-client-f7-create", IdempotencyKey: "share-grant-idempotency-create-f7", TenantID: tenantID, UserID: ownerID, SessionID: "f7000000-0000-4000-8000-000000000016"}, GranteeUserID: granteeID, ResourceKind: "project", ResourceID: projectID, ResourceRevision: "1", Scope: []string{"read"}}
	created, err := service.Create(ctx, command)
	if err != nil || created.Status != "active" || created.Version != 1 || created.Replayed {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	replayed, err := service.Create(ctx, command)
	if err != nil || !replayed.Replayed || replayed.ID != created.ID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if allowed, err := service.Authorize(ctx, read); err != nil || !allowed {
		t.Fatalf("read allowed=%v err=%v", allowed, err)
	}
	review := read
	review.Scope = "review"
	if allowed, err := service.Authorize(ctx, review); err != nil || allowed {
		t.Fatalf("review allowed=%v err=%v", allowed, err)
	}
	wrongRevision := read
	wrongRevision.ResourceRevision = "2"
	if allowed, err := service.Authorize(ctx, wrongRevision); err != nil || allowed {
		t.Fatalf("revision allowed=%v err=%v", allowed, err)
	}
	crossTenant := read
	crossTenant.TenantID, crossTenant.GranteeUserID = otherTenantID, otherID
	if allowed, err := service.Authorize(ctx, crossTenant); err != nil || allowed {
		t.Fatalf("cross tenant allowed=%v err=%v", allowed, err)
	}
	crossGrant := command
	crossGrant.IdempotencyKey = "share-grant-idempotency-cross-f7"
	crossGrant.ClientRequestID = "share-client-f7-cross"
	crossGrant.GranteeUserID = otherID
	if _, err = service.Create(ctx, crossGrant); !errors.Is(err, productapi.ErrPermissionDenied) {
		t.Fatalf("cross grant error=%v", err)
	}
	revoked, err := service.Revoke(ctx, productapi.RevokeShareGrantCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "f7000000-0000-4000-8000-000000000017", ClientRequestID: "share-client-f7-revoke", IdempotencyKey: "share-grant-idempotency-revoke-f7", TenantID: tenantID, UserID: ownerID, SessionID: "f7000000-0000-4000-8000-000000000016"}, GrantID: created.ID, Reason: "Review engagement ended", ExpectedGrantVersion: 1})
	if err != nil || revoked.Status != "revoked" || revoked.Version != 2 {
		t.Fatalf("revoked=%#v err=%v", revoked, err)
	}
	if allowed, err := service.Authorize(ctx, read); err != nil || allowed {
		t.Fatalf("after revoke allowed=%v err=%v", allowed, err)
	}
	var active, createdEvents, revokedEvents int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.share_grants WHERE tenant_id=$1 AND id=$2 AND version=2 AND revoked_at=$3),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='ShareGrantCreated'),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='ShareGrantRevoked')`, tenantID, created.ID, now).Scan(&active, &createdEvents, &revokedEvents); err != nil || active != 1 || createdEvents != 1 || revokedEvents != 1 {
		t.Fatalf("grant=%d created_events=%d revoked_events=%d err=%v", active, createdEvents, revokedEvents, err)
	}
}
