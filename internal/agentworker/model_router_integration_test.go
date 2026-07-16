//go:build integration

package agentworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/toolregistry"
	"github.com/langshift/lites/internal/toolworker"
)

type overlayAdmissionStub struct{ decision toolworker.PolicyDecision }

func (stub overlayAdmissionStub) EvaluateSnapshot(context.Context, string, toolregistry.Snapshot) (toolworker.PolicyDecision, error) {
	return stub.decision, nil
}

func TestPostgresRouteResourcesSelectsFundedBucketAndExactBYOKVersion(t *testing.T) {
	ctx := context.Background()
	admin := contextPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	agent := contextPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agent.Close()
	const (
		userID       = "c8000000-0000-4000-8000-000000000001"
		tenantID     = "c8000000-0000-4000-8000-000000000002"
		bucketID     = "c8000000-0000-4000-8000-000000000003"
		credentialID = "c8000000-0000-4000-8000-000000000004"
	)
	now := time.Now().UTC().Truncate(time.Microsecond)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'route-resource@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Route Resource Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO contracts.credit_buckets(id,tenant_id,bucket_kind,granted_units,starts_at,expires_at,created_at,updated_at) VALUES($1,$2,'llm',100000,$3,$4,$5,$5)`, []any{bucketID, tenantID, now.Add(-time.Hour), now.Add(time.Hour), now}},
		{`INSERT INTO product.byok_credential_versions(tenant_id,credential_id,version,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at) VALUES($1,$2,1,$3,'openai','api.openai.com','vault://byok/route/v1','1','active',$4)`, []any{tenantID, credentialID, userID, now}},
		{`INSERT INTO product.byok_credentials(id,tenant_id,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at) VALUES($1,$2,$3,'openai','api.openai.com','vault://byok/route/v1','1','active',$4)`, []any{credentialID, tenantID, userID, now}},
		{`INSERT INTO product.byok_credential_versions(tenant_id,credential_id,version,user_id,provider_id,bound_host,secret_ref,secret_version,status,last_validated_at) VALUES($1,$2,2,$3,'openai','api.openai.com','vault://byok/route/v2','2','active',$4)`, []any{tenantID, credentialID, userID, now}},
		{`UPDATE product.byok_credentials SET version=2,secret_ref='vault://byok/route/v2',secret_version='2',updated_at=$1 WHERE tenant_id=$2 AND id=$3`, []any{now, tenantID, credentialID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	resolver := PostgresRouteResources{Pool: agent}
	decision, err := resolver.ResolveRouteResources(ctx, RouteResourceRequest{TenantID: tenantID, UserID: userID, ProviderID: "openai", BoundHost: "api.openai.com", CredentialMode: "require_byok", ReservedUnits: 1000, RequiredUntil: now.Add(30 * time.Minute)})
	if err != nil || decision.BucketID != bucketID || decision.BYOK == nil || decision.BYOK.CredentialID != credentialID || decision.BYOK.Version != 2 || decision.BYOK.SecretVersion != "2" {
		t.Fatalf("decision=%#v error=%v", decision, err)
	}
	managed, err := resolver.ResolveRouteResources(ctx, RouteResourceRequest{TenantID: tenantID, UserID: userID, ProviderID: "anthropic", BoundHost: "api.anthropic.com", CredentialMode: "managed", ReservedUnits: 1000, RequiredUntil: now.Add(30 * time.Minute)})
	if err != nil || managed.BucketID != bucketID || managed.BYOK != nil {
		t.Fatalf("managed=%#v error=%v", managed, err)
	}
	if _, err = resolver.ResolveRouteResources(ctx, RouteResourceRequest{TenantID: tenantID, UserID: userID, ProviderID: "anthropic", BoundHost: "api.anthropic.com", CredentialMode: "require_byok", ReservedUnits: 1000, RequiredUntil: now.Add(30 * time.Minute)}); !errors.Is(err, ErrRouteResources) {
		t.Fatalf("missing BYOK error=%v", err)
	}
	if _, err = resolver.ResolveRouteResources(ctx, RouteResourceRequest{TenantID: tenantID, UserID: userID, ProviderID: "openai", BoundHost: "api.openai.com", CredentialMode: "managed", ReservedUnits: 100001, RequiredUntil: now.Add(30 * time.Minute)}); !errors.Is(err, ErrRouteResources) {
		t.Fatalf("insufficient credit error=%v", err)
	}
}

func TestPostgresToolAdmissionUsesCurrentReviewedMembership(t *testing.T) {
	ctx := context.Background()
	admin := contextPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	agent := contextPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agent.Close()
	const (
		userID       = "d8000000-0000-4000-8000-000000000001"
		tenantID     = "d8000000-0000-4000-8000-000000000002"
		membershipID = "d8000000-0000-4000-8000-000000000003"
	)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'tool-admission@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Tool Admission Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$4)`, []any{membershipID, tenantID, userID, now}},
	} {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	_, snapshot := agentToolRegistry(t, toolregistry.ApprovalNone, "read_only")
	snapshot.Descriptor.RequiredPermissions = []string{"private_work.read"}
	admission := PostgresToolAdmission{Pool: agent, Overlay: overlayAdmissionStub{decision: toolworker.PolicyDecision{Allowed: true, SnapshotID: "policy-1", SnapshotHash: strings.Repeat("a", 64), OverlayVersion: 7}}}
	request := ToolAdmissionRequest{TenantID: tenantID, UserID: userID, RunID: "run-1", Snapshot: snapshot, NormalizedInputHash: strings.Repeat("b", 64), RequestHash: strings.Repeat("c", 64)}
	decision, err := admission.Evaluate(ctx, request)
	if err != nil || decision.PermissionSnapshot != "membership:"+membershipID+":v1:role:member" || decision.Decision != "allow" || decision.EffectKey != "" {
		t.Fatalf("decision=%#v error=%v", decision, err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.memberships SET version=2,status='left',deactivated_at=$1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now.Add(time.Second), tenantID, membershipID); err != nil {
		t.Fatal(err)
	}
	if _, err = admission.Evaluate(ctx, request); !errors.Is(err, ErrToolPermission) {
		t.Fatalf("revoked permission error=%v", err)
	}
}
