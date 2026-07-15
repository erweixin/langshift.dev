//go:build integration

package toolworker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSandboxBrokerUsesLeastPrivilegeAuthorityAndPlacementPools(t *testing.T) {
	ctx := context.Background()
	admin := sandboxBrokerPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	agent := sandboxBrokerPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agent.Close()
	runtimePool := sandboxBrokerPool(t, ctx, "LITES_TEST_RUNTIME_DATABASE_URL")
	defer runtimePool.Close()
	const (
		userID   = "ca510000-0000-4000-8000-000000000001"
		tenantID = "ca510000-0000-4000-8000-000000000002"
		policyID = "ca510000-0000-4000-8000-000000000003"
		eventID  = "ca510000-0000-4000-8000-000000000004"
		epoch    = "ca510000-0000-4000-8000-000000000005"
		hostID   = "runtime-host-ca51"
	)
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'sandbox-broker@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Sandbox Broker','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	tx, err := admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	kernel, rootfs := "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64)
	empty, image := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "sha256:"+strings.Repeat("a", 64)
	if _, err = tx.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,1,'RuntimePolicySnapshotCreated',1,'runtime_policy',$4,1,$5,$6,$6,'{"kind":"system"}',$4,'encrypted://sandbox-broker/policy','policy')`, eventID, tenantID, userID, policyID, epoch, now); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.runtime_policy_snapshots(id,tenant_id,snapshot_key,version,policy_hash,trust_tier,isolation_kind,network_mode,network_policy_hash,secret_mode,secret_scope_hash,workspace_mode,image_digest,kernel_digest,rootfs_digest,vcpu_count,memory_mib,disk_mib,pids_max,maximum_duration_seconds,idle_timeout_seconds,kill_grace_seconds,approval_required,policy_manifest,created_event_id,created_at) VALUES($1,$2,'runtime-policy:provider:v1',1,$3,'untrusted','firecracker','none',$4,'none',$4,'none',$5,$6,$7,1,128,64,32,30,15,5,false,'{"schema_version":1}',$8,$9)`, policyID, tenantID, strings.Repeat("d", 64), empty, image, kernel, rootfs, eventID, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO agent.runtime_hosts(host_id,version,pool_key,status,architecture,availability_zone,firecracker_version,kernel_catalog_hash,rootfs_catalog_hash,scratch_template_digest,control_token_hash,capacity_vcpu,capacity_memory_mib,capacity_disk_mib,capacity_sessions,heartbeat_at,heartbeat_deadline,created_at,updated_at) VALUES($1,1,'runtime-untrusted','active','x86_64','zone-a','1.15.1',$2,$3,$4,$5,8,16384,65536,16,$6,$7,$6,$6)`, hostID, kernel, rootfs, "sha256:"+strings.Repeat("e", 64), []byte(strings.Repeat("f", 32)), now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	registry, snapshot := workerRegistryWithTrust(t, "read_only", "30s", "untrusted")
	execution := registryExecution(t, registry, snapshot)
	execution.Claim.TenantID, execution.Claim.ToolCallID, execution.Claim.RunID = tenantID, "tool-ca51", "run-ca51"
	broker := PostgresSandboxBroker{Pool: agent, PlacementPool: runtimePool, Endpoints: map[string]SandboxHostEndpoint{hostID: {URL: "https://runtime-host-ca51.invalid", KernelDigest: kernel, RootFSDigest: rootfs}}, Now: func() time.Time { return now }}
	policy, err := broker.selectPolicy(ctx, SandboxAcquireRequest{Execution: execution, Snapshot: snapshot, RequestID: "sandbox:attempt-ca51"})
	if err != nil || policy.ID != policyID {
		t.Fatalf("policy=%#v err=%v", policy, err)
	}
	placed, err := broker.place(ctx, policy)
	if err != nil || placed != hostID {
		t.Fatalf("host=%q err=%v", placed, err)
	}
}

func sandboxBrokerPool(t *testing.T, ctx context.Context, environment string) *pgxpool.Pool {
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
