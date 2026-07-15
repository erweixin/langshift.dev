//go:build integration

package toolworker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresPolicyEvaluatorCombinesPlatformAndTenantChains(t *testing.T) {
	ctx := context.Background()
	admin := policyPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	agent := policyPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agent.Close()
	const (
		userID   = "ea510000-0000-4000-8000-000000000001"
		tenantID = "ea510000-0000-4000-8000-000000000002"
	)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'tool-policy@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Tool Policy Tenant','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	registry, snapshot := workerRegistry(t, "read_only")
	execution := registryExecution(t, registry, snapshot).ProposedExecution
	execution.Command.TenantID = tenantID
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	evaluator := PostgresPolicyEvaluator{Pool: agent, Registry: &registry, Now: func() time.Time { return now }}
	baseline, err := evaluator.Evaluate(ctx, execution)
	if err != nil || !baseline.Allowed || baseline.OverlayVersion != 1 {
		t.Fatalf("baseline=%#v error=%v", baseline, err)
	}
	insertPlatform := `INSERT INTO agent.platform_tool_execution_overlays(id,tool_name,descriptor_snapshot_id,descriptor_hash,policy_version,decision,reason_code,approved_by,approval_evidence_hash,effective_at) VALUES($1,$2,$3,$4,$5,$6,$7,'security-release-board',$8,$9)`
	if _, err = admin.Exec(ctx, insertPlatform, "ea510000-0000-4000-8000-000000000010", snapshot.Descriptor.Name, snapshot.SnapshotID, snapshot.Hash, 1, "deny", "credential_exposure", strings.Repeat("a", 64), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	denied, err := evaluator.Evaluate(ctx, execution)
	if err != nil || denied.Allowed || denied.ReasonCode != "platform:credential_exposure" || denied.SnapshotHash == baseline.SnapshotHash {
		t.Fatalf("platform denied=%#v error=%v", denied, err)
	}
	insertTenant := `INSERT INTO agent.tenant_tool_execution_overlays(id,tenant_id,tool_name,descriptor_snapshot_id,descriptor_hash,policy_version,decision,reason_code,approved_by,approval_evidence_hash,effective_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`
	if _, err = admin.Exec(ctx, insertPlatform, "ea510000-0000-4000-8000-000000000011", snapshot.Descriptor.Name, snapshot.SnapshotID, snapshot.Hash, 2, "allow", "incident_resolved", strings.Repeat("b", 64), now.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, insertTenant, "ea510000-0000-4000-8000-000000000012", tenantID, snapshot.Descriptor.Name, snapshot.SnapshotID, snapshot.Hash, 1, "deny", "tenant_policy", userID, strings.Repeat("c", 64), now.Add(-20*time.Second)); err != nil {
		t.Fatal(err)
	}
	tenantDenied, err := evaluator.Evaluate(ctx, execution)
	if err != nil || tenantDenied.Allowed || tenantDenied.ReasonCode != "tenant:tenant_policy" || tenantDenied.OverlayVersion != 2 {
		t.Fatalf("tenant denied=%#v error=%v", tenantDenied, err)
	}
	if _, err = admin.Exec(ctx, insertTenant, "ea510000-0000-4000-8000-000000000013", tenantID, snapshot.Descriptor.Name, snapshot.SnapshotID, snapshot.Hash, 3, "allow", "tenant_review_complete", userID, strings.Repeat("d", 64), now.Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	allowed, err := evaluator.Evaluate(ctx, execution)
	if err != nil || !allowed.Allowed || allowed.OverlayVersion != 3 || allowed.SnapshotHash == tenantDenied.SnapshotHash {
		t.Fatalf("allowed=%#v error=%v", allowed, err)
	}
}

func policyPool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Skip(name + " is not configured")
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
