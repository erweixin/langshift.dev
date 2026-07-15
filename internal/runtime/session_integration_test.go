//go:build integration

package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeSessionProtocolBindsPolicyEventFenceAndTenant(t *testing.T) {
	ctx := context.Background()
	admin := runtimePool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	service := runtimePool(t, ctx, "LITES_TEST_RUNTIME_DATABASE_URL")
	defer service.Close()

	const (
		userID        = "10000000-0000-0000-0000-000000009101"
		tenantID      = "20000000-0000-0000-0000-000000009101"
		otherUserID   = "10000000-0000-0000-0000-000000009102"
		otherTenantID = "20000000-0000-0000-0000-000000009102"
		runID         = "30000000-0000-0000-0000-000000009101"
		toolID        = "40000000-0000-0000-0000-000000009101"
		commandID     = "50000000-0000-0000-0000-000000009101"
		outboxID      = "50000000-0000-0000-0000-000000009102"
		jobID         = "50000000-0000-0000-0000-000000009103"
		attemptID     = "60000000-0000-0000-0000-000000009101"
		policyID      = "70000000-0000-0000-0000-000000009101"
		sessionID     = "80000000-0000-0000-0000-000000009101"
		epoch         = "90000000-0000-0000-0000-000000009101"
		correlation   = "a0000000-0000-0000-0000-000000009101"
	)
	now := time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC)
	leaseExpires := now.Add(5 * time.Minute)
	digest := "sha256:" + strings.Repeat("a", 64)
	empty := emptyPolicyHash()
	leaseHash := bytesOf(0x41)
	nonceHash := bytesOf(0x42)
	policyHash := strings.Repeat("b", 64)
	requestHash := strings.Repeat("c", 64)

	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES ($1,'runtime@lites.invalid',$3,'en','active'),($2,'runtime-other@lites.invalid',$3,'en','active')`, userID, otherUserID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Runtime','active','US',$3),($2,'personal','Runtime Other','active','US',$4)`, tenantID, otherTenantID, userID, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,due_at,profile_snapshot_id,budget_snapshot) VALUES($1,$2,$3,'30000000-0000-0000-0000-000000009199','waiting_tool',1,$4,'runtime@v1','{}')`, runID, tenantID, userID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO agent.outbox(id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at,published_at) VALUES($1,$2,$3,'ExecuteToolCall','tool_call',$4,$5,'encrypted://runtime/command','runtime-command','published',$6,$6)`, outboxID, tenantID, commandID, toolID, epoch, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at) VALUES($1,$2,$3,'interactive','runtime-untrusted',100,1,5,'running',$4,$5,$4)`, jobID, tenantID, commandID, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at) VALUES($1,$2,$3,$4,7,$5,$6,'runtime-worker-1','running',$7)`, attemptID, tenantID, jobID, commandID, leaseHash, leaseExpires, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,effect_key,active_command_id,active_attempt_id,current_fence,lease_token_hash,lease_expires_at) VALUES($1,$2,$3,$4,'executing',1,'code_execute','code_execute@v1','encrypted://runtime/input',$5,'idempotent_write','runtime-effect',$6,$7,7,$8,$9)`, toolID, tenantID, userID, runID, requestHash, commandID, attemptID, leaseHash, leaseExpires); err != nil {
		t.Fatal(err)
	}

	tx, err := admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	policyEvent := "b0000000-0000-0000-0000-000000009101"
	sessionEvent := "b0000000-0000-0000-0000-000000009102"
	if _, err = tx.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,1,'RuntimePolicySnapshotCreated',1,'runtime_policy',$4,1,$5,$6,$6,'{"kind":"system"}',$7,'encrypted://runtime/policy','policy-event'),($8,$2,$3,2,'RuntimeSessionRequested',1,'runtime_session',$9,1,$5,$6,$6,'{"kind":"system"}',$7,'encrypted://runtime/session','session-event')`, policyEvent, tenantID, userID, policyID, epoch, now, correlation, sessionEvent, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.runtime_policy_snapshots(id,tenant_id,snapshot_key,version,policy_hash,trust_tier,isolation_kind,network_mode,network_policy_hash,secret_mode,secret_scope_hash,workspace_mode,image_digest,kernel_digest,rootfs_digest,vcpu_count,memory_mib,disk_mib,pids_max,maximum_duration_seconds,idle_timeout_seconds,kill_grace_seconds,approval_required,policy_manifest,created_event_id,created_at) VALUES($1,$2,'runtime-policy:untrusted:v1',1,$3,'untrusted','firecracker','none',$4,'none',$4,'none',$5,$5,$5,2,512,4096,256,600,60,5,false,'{"schema_version":"1"}',$6,$7)`, policyID, tenantID, policyHash, empty, digest, policyEvent, now); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.runtime_sessions(id,tenant_id,user_id,run_id,tool_call_id,version,status,policy_snapshot_id,policy_snapshot_key,policy_hash,trust_tier,isolation_kind,workspace_mode,network_policy_hash,secret_scope_hash,request_hash,capability_nonce_hash,command_id,execution_attempt_id,execution_fence,execution_lease_hash,execution_lease_expires_at,requested_at,execution_deadline,created_event_id,last_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,1,'requested',$6,'runtime-policy:untrusted:v1',$7,'untrusted','firecracker','none',$8,$8,$9,$10,$11,$12,7,$13,$14,$15,$16,$17,$17,$15,$15)`, sessionID, tenantID, userID, runID, toolID, policyID, policyHash, empty, requestHash, nonceHash, commandID, attemptID, leaseHash, leaseExpires, now, now.Add(10*time.Minute), sessionEvent); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	provisionEvent := "b0000000-0000-0000-0000-000000009103"
	provisionAttempt := "60000000-0000-0000-0000-000000009102"
	tx, err = admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,causation_id,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,3,'RuntimeSessionProvisioningStarted',1,'runtime_session',$4,2,$5,$6,$6,'{"kind":"system"}',$7,$8,'encrypted://runtime/provision','provision-event')`, provisionEvent, tenantID, userID, sessionID, epoch, now.Add(time.Second), sessionEvent, correlation); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE agent.runtime_sessions SET version=2,status='provisioning',host_id='runtime-host-1',machine_id='runtime-019f60b2',guest_cid=42,provision_attempt_id=$1,provision_fence=1,provision_lease_hash=$2,provision_lease_expires_at=$3,provisioning_at=$4,last_event_id=$5,updated_at=$4 WHERE tenant_id=$6 AND id=$7 AND version=1 AND status='requested'`, provisionAttempt, bytesOf(0x43), now.Add(time.Minute), now.Add(time.Second), provisionEvent, tenantID, sessionID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err = admin.Exec(ctx, `UPDATE agent.runtime_sessions SET version=3,status='terminated',policy_hash=$1 WHERE tenant_id=$2 AND id=$3`, strings.Repeat("d", 64), tenantID, sessionID); err == nil {
		t.Fatal("runtime session accepted an illegal transition and immutable policy mutation")
	}

	serviceTx, err := service.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer serviceTx.Rollback(ctx)
	if _, err = serviceTx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, otherTenantID); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err = serviceTx.QueryRow(ctx, `SELECT count(*) FROM agent.runtime_sessions`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("cross-tenant runtime sessions = %d, %v", visible, err)
	}
}

func runtimePool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("missing %s", name)
	}
	pool, err := pgxpool.New(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func bytesOf(value byte) []byte {
	return []byte(strings.Repeat(string([]byte{value}), 32))
}
