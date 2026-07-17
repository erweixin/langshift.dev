//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	productapi "github.com/langshift/lites/internal/product/api"
)

func TestMissionQueryServicePaginatesOneSnapshotAndEnforcesOwnerScope(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	userID := "c6500000-0000-4000-8000-000000000001"
	otherUserID := "c6500000-0000-4000-8000-000000000002"
	tenantID := "c6500000-0000-4000-8000-000000000003"
	roleID := "c6500000-0000-4000-8000-000000000004"
	if _, err := admin.Exec(ctx, `DELETE FROM identity.tenants WHERE id=$1 OR owner_user_id IN ($2,$3)`, tenantID, userID, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM identity.users WHERE id IN ($1,$2)`, userID, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'mission-query-owner@example.invalid','en','active'),($2,'mission-query-other@example.invalid','en','active')`, userID, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Mission Query Tenant','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'mission-query-role',1,'active','{}','en','{}')`, roleID, tenantID); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.July, 16, 16, 0, 0, 0, time.UTC)
	missionIDs := make([]string, 51)
	for index := range missionIDs {
		missionIDs[index] = fmt.Sprintf("c6500000-0000-4000-8001-%012d", index+1)
		if _, err := admin.Exec(ctx, `INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash,created_at,updated_at) VALUES($1,$2,$3,'active',$4,0,$5,$6,$6)`, missionIDs[index], tenantID, userID, roleID, fmt.Sprintf("query-claims-%d", index), base.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `INSERT INTO product.mission_focuses(tenant_id,user_id,mission_id,focus_version) VALUES($1,$2,$3,9)`, tenantID, userID, missionIDs[50]); err != nil {
		t.Fatal(err)
	}
	service := MissionQueryService{Pool: pool, CursorKey: bytes.Repeat([]byte{0xe5}, 32)}
	first, err := service.List(ctx, productapi.ListMissionsQuery{TenantID: tenantID, UserID: userID})
	if err != nil || len(first.Items) != 50 || first.NextCursor == nil || first.Focus.Version != 9 || first.Focus.MissionID == nil || *first.Focus.MissionID != missionIDs[50] || !first.Items[0].Focused {
		t.Fatalf("first items=%d focus=%#v cursor=%v err=%v", len(first.Items), first.Focus, first.NextCursor, err)
	}
	second, err := service.List(ctx, productapi.ListMissionsQuery{TenantID: tenantID, UserID: userID, Cursor: *first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].ID != missionIDs[0] {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if _, err = service.List(ctx, productapi.ListMissionsQuery{TenantID: tenantID, UserID: otherUserID, Cursor: *first.NextCursor}); !errors.Is(err, productapi.ErrValidation) {
		t.Fatalf("cross-owner cursor error=%v", err)
	}
	resource, err := service.Get(ctx, productapi.GetMissionQuery{TenantID: tenantID, UserID: userID, MissionID: missionIDs[50]})
	if err != nil || !resource.Focused || resource.Version != 1 {
		t.Fatalf("resource=%#v err=%v", resource, err)
	}
	if _, err = service.Get(ctx, productapi.GetMissionQuery{TenantID: tenantID, UserID: otherUserID, MissionID: missionIDs[50]}); !errors.Is(err, productapi.ErrResourceNotFound) {
		t.Fatalf("cross-owner get error=%v", err)
	}
}
