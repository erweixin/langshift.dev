//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	runtimecontract "github.com/langshift/lites/internal/runtime"
)

type fixedEpoch string

func (epoch fixedEpoch) CurrentStoreEpoch(context.Context) (string, error) { return string(epoch), nil }

func TestBeginProvisionAtomicallyBindsCapabilityEventSessionAndCapacity(t *testing.T) {
	ctx := context.Background()
	admin := integrationPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	service := integrationPool(t, ctx, "LITES_TEST_RUNTIME_DATABASE_URL")
	defer service.Close()

	const (
		userID    = "10000000-0000-0000-0000-000000009201"
		tenantID  = "20000000-0000-0000-0000-000000009201"
		runID     = "30000000-0000-0000-0000-000000009201"
		toolID    = "40000000-0000-0000-0000-000000009201"
		commandID = "50000000-0000-0000-0000-000000009201"
		outboxID  = "50000000-0000-0000-0000-000000009202"
		jobID     = "50000000-0000-0000-0000-000000009203"
		attemptID = "60000000-0000-0000-0000-000000009201"
		policyID  = "70000000-0000-0000-0000-000000009201"
		sessionID = "80000000-0000-0000-0000-000000009201"
		epoch     = "90000000-0000-0000-0000-000000009201"
		hostID    = "runtime-host-9201"
	)
	now := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	policyHash := strings.Repeat("b", 64)
	requestHash := strings.Repeat("c", 64)
	digest := "sha256:" + strings.Repeat("a", 64)
	emptySum := sha256.Sum256(nil)
	empty := "sha256:" + hex.EncodeToString(emptySum[:])
	executionLease := bytes.Repeat([]byte{0x61}, 32)
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 16))
	nonceDigest, err := runtimecontract.CapabilityNonceDigest(nonce)
	if err != nil {
		t.Fatal(err)
	}

	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'runtime-store@lites.invalid',$2,'en','active')`, []any{userID, now}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Runtime Store','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,due_at,profile_snapshot_id,budget_snapshot) VALUES($1,$2,$3,'30000000-0000-0000-0000-000000009299','waiting_tool',1,$4,'runtime@v1','{}')`, []any{runID, tenantID, userID, now.Add(time.Hour)}},
		{`INSERT INTO agent.outbox(id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at,published_at) VALUES($1,$2,$3,'ExecuteToolCall','tool_call',$4,$5,'encrypted://runtime-store/command','runtime-store-command','published',$6,$6)`, []any{outboxID, tenantID, commandID, toolID, epoch, now}},
		{`INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at) VALUES($1,$2,$3,'interactive','runtime-untrusted',100,1,5,'running',$4,$5,$4)`, []any{jobID, tenantID, commandID, now, now.Add(time.Hour)}},
		{`INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at) VALUES($1,$2,$3,$4,7,$5,$6,'runtime-worker-9201','running',$7)`, []any{attemptID, tenantID, jobID, commandID, executionLease, now.Add(5 * time.Minute), now}},
		{`INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,effect_key,active_command_id,active_attempt_id,current_fence,lease_token_hash,lease_expires_at) VALUES($1,$2,$3,$4,'executing',1,'code_execute','code_execute@v1','encrypted://runtime-store/input',$5,'idempotent_write','runtime-store-effect',$6,$7,7,$8,$9)`, []any{toolID, tenantID, userID, runID, requestHash, commandID, attemptID, executionLease, now.Add(5 * time.Minute)}},
	}
	for _, statement := range statements {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	policyEvent := "b0000000-0000-0000-0000-000000009201"
	sessionEvent := "b0000000-0000-0000-0000-000000009202"
	tx, err := admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,1,'RuntimePolicySnapshotCreated',1,'runtime_policy',$4,1,$5,$6,$6,'{"kind":"system"}','a0000000-0000-0000-0000-000000009201','encrypted://runtime-store/policy','policy-event'),($7,$2,$3,2,'RuntimeSessionRequested',1,'runtime_session',$8,1,$5,$6,$6,'{"kind":"system"}','a0000000-0000-0000-0000-000000009201','encrypted://runtime-store/session','session-event')`, policyEvent, tenantID, userID, policyID, epoch, now, sessionEvent, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.event_cursors(tenant_id,user_id,last_seq) VALUES($1,$2,2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.runtime_policy_snapshots(id,tenant_id,snapshot_key,version,policy_hash,trust_tier,isolation_kind,network_mode,network_policy_hash,secret_mode,secret_scope_hash,workspace_mode,image_digest,kernel_digest,rootfs_digest,vcpu_count,memory_mib,disk_mib,pids_max,maximum_duration_seconds,idle_timeout_seconds,kill_grace_seconds,approval_required,policy_manifest,created_event_id,created_at) VALUES($1,$2,'runtime-policy:untrusted:v1',1,$3,'untrusted','firecracker','none',$4,'none',$4,'none',$5,$5,$5,2,512,4096,256,600,60,5,false,'{"schema_version":"1"}',$6,$7)`, policyID, tenantID, policyHash, empty, digest, policyEvent, now); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.runtime_sessions(id,tenant_id,user_id,run_id,tool_call_id,version,status,policy_snapshot_id,policy_snapshot_key,policy_hash,trust_tier,isolation_kind,workspace_mode,network_policy_hash,secret_scope_hash,request_hash,capability_nonce_hash,command_id,execution_attempt_id,execution_fence,execution_lease_hash,execution_lease_expires_at,requested_at,execution_deadline,created_event_id,last_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,1,'requested',$6,'runtime-policy:untrusted:v1',$7,'untrusted','firecracker','none',$8,$8,$9,$10,$11,$12,7,$13,$14,$15,$16,$17,$17,$15,$15)`, sessionID, tenantID, userID, runID, toolID, policyID, policyHash, empty, requestHash, nonceDigest[:], commandID, attemptID, executionLease, now.Add(5*time.Minute), now, now.Add(10*time.Minute), sessionEvent); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var hostVersion int64
	controlHash := bytes.Repeat([]byte{0x63}, 32)
	if err = admin.QueryRow(ctx, `SELECT agent.runtime_register_host($1,'runtime-untrusted','x86_64','us-east-1a',$2,$2,$2,$3,2,512,4096,1,$4,$5)`, hostID, digest, controlHash, now, now.Add(10*time.Minute)).Scan(&hostVersion); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT agent.runtime_set_host_status($1,$2,$3,'active',$4,$5)`, hostID, hostVersion, controlHash, now.Add(time.Millisecond), now.Add(10*time.Minute)).Scan(&hostVersion); err != nil {
		t.Fatal(err)
	}

	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x64}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	claims := runtimecontract.CapabilityClaims{
		Purpose: "runtime_session", Issuer: "event-service", Audience: "runtime-manager",
		TenantID: tenantID, UserID: userID, RunID: runID, ToolCallID: toolID, CommandID: commandID, AttemptID: attemptID, Fence: 7,
		PolicySnapshotID: "runtime-policy:untrusted:v1", PolicyHash: policyHash, WorkspaceMode: runtimecontract.WorkspaceNone,
		NetworkPolicyHash: empty, SecretScopeHash: empty, LeaseTokenHash: base64.RawURLEncoding.EncodeToString(executionLease), RequestHash: requestHash,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(2 * time.Minute).Unix(), Nonce: nonce,
	}
	capability, err := runtimecontract.SignCapability(claims, "runtime-key-v1", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{
		Pool: service, Appender: eventpostgres.Appender{Now: func() time.Time { return now.Add(time.Second) }}, Epochs: fixedEpoch(epoch), StoreEpoch: epoch,
		IDKey: bytes.Repeat([]byte{0x65}, 32), TokenPepper: bytes.Repeat([]byte{0x66}, 32),
		Verifier: runtimecontract.CapabilityVerifier{Issuer: "event-service", Audience: "runtime-manager", Keys: map[string]ed25519.PublicKey{"runtime-key-v1": publicKey}, MaximumTTL: 5 * time.Minute},
		Now:      func() time.Time { return now.Add(time.Second) },
	}
	lockTx, err := service.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lockTx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	var executionRight string
	if err = lockTx.QueryRow(ctx, `SELECT agent.runtime_lock_execution_right($1,$2,NULL,NULL,$3)`, tenantID, sessionID, now.Add(time.Second)).Scan(&executionRight); err != nil || executionRight != "valid" {
		t.Fatalf("runtime_lock_execution_right() = %v, %v", executionRight, err)
	}
	contender, err := admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = contender.Exec(ctx, `SET LOCAL lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	var lockedTool string
	err = contender.QueryRow(ctx, `SELECT id::text FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, toolID).Scan(&lockedTool)
	var lockError *pgconn.PgError
	if !errors.As(err, &lockError) || lockError.Code != "55P03" {
		t.Fatalf("execution right did not lock tool call: %v", err)
	}
	_ = contender.Rollback(ctx)
	if err = lockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	provisionLease, err := store.IssueProvisionLease()
	if err != nil {
		t.Fatal(err)
	}
	command := ProvisionCommand{
		CapabilityToken: capability, HostID: hostID, MachineID: "runtime-store-9201", GuestCID: 92,
		ProvisionLease: provisionLease, ProvisionLeaseExpiresAt: now.Add(time.Minute),
		Payload: PayloadPointer{Ref: "encrypted://runtime-store/provision", Hash: "runtime-store-provision"}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: "a0000000-0000-0000-0000-000000009201",
	}
	unauthorizedClaims := claims
	unauthorizedClaims.ApprovalID = "a0000000-0000-0000-0000-000000009299"
	unauthorizedClaims.ApprovalVersion = 2
	command.CapabilityToken, err = runtimecontract.SignCapability(unauthorizedClaims, "runtime-key-v1", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginProvision(ctx, command); !errors.Is(err, ErrApproval) {
		t.Fatalf("unbound approval = %v", err)
	}
	command.CapabilityToken = capability
	result, err := store.BeginProvision(ctx, command)
	if err != nil || result.Replayed || result.SessionID != sessionID || result.Version != 2 || result.Policy.RootFSDigest != digest {
		t.Fatalf("BeginProvision() = %#v, %v", result, err)
	}
	replayed, err := store.BeginProvision(ctx, command)
	if err != nil || !replayed.Replayed || replayed.AllocationID != result.AllocationID || replayed.ProvisionAttemptID != result.ProvisionAttemptID {
		t.Fatalf("replayed BeginProvision() = %#v, %v", replayed, err)
	}
	var allocations, provisioningEvents, allocatedSessions int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.runtime_allocations WHERE tenant_id=$1 AND session_id=$2`, tenantID, sessionID).Scan(&allocations); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='RuntimeSessionProvisioningStarted'`, tenantID, sessionID).Scan(&provisioningEvents); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT allocated_sessions FROM agent.runtime_hosts WHERE host_id=$1`, hostID).Scan(&allocatedSessions); err != nil {
		t.Fatal(err)
	}
	if allocations != 1 || provisioningEvents != 1 || allocatedSessions != 1 {
		t.Fatalf("replay duplicated facts allocations=%d events=%d capacity=%d", allocations, provisioningEvents, allocatedSessions)
	}

	claims.Fence++
	tamperedCapability, err := runtimecontract.SignCapability(claims, "runtime-key-v1", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	command.CapabilityToken = tamperedCapability
	if _, err = store.BeginProvision(ctx, command); !errors.Is(err, ErrCapabilityBinding) {
		t.Fatalf("signed stale fence = %v", err)
	}
}

func integrationPool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
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
