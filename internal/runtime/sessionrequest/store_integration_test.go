//go:build integration

package sessionrequest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	runtimecontract "github.com/langshift/lites/internal/runtime"
	"github.com/langshift/lites/internal/security/opaque"
)

type fixedEpoch string

func (epoch fixedEpoch) CurrentStoreEpoch(context.Context) (string, error) { return string(epoch), nil }

func TestRequestAtomicallyBindsFencedToolPolicyEventAndReplay(t *testing.T) {
	ctx := context.Background()
	admin := requestPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	agent := requestPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer agent.Close()
	const (
		userID          = "11000000-0000-4000-8000-000000005101"
		tenantID        = "21000000-0000-4000-8000-000000005101"
		runID           = "31000000-0000-4000-8000-000000005101"
		toolID          = "41000000-0000-4000-8000-000000005101"
		commandID       = "51000000-0000-4000-8000-000000005101"
		commandOutboxID = "51000000-0000-4000-8000-000000005102"
		jobID           = "51000000-0000-4000-8000-000000005103"
		attemptID       = "61000000-0000-4000-8000-000000005101"
		inboxID         = "61000000-0000-4000-8000-000000005102"
		policyID        = "71000000-0000-4000-8000-000000005101"
		policyEventID   = "71000000-0000-4000-8000-000000005102"
		epoch           = "91000000-0000-4000-8000-000000005101"
		correlationID   = "a1000000-0000-4000-8000-000000005101"
	)
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	requestHash := strings.Repeat("c", 64)
	policyHash := strings.Repeat("b", 64)
	imageDigest := "sha256:" + strings.Repeat("a", 64)
	emptySum := sha256.Sum256(nil)
	empty := "sha256:" + hex.EncodeToString(emptySum[:])
	manager := opaque.Manager{Purpose: "runtime-session-request-integration", Pepper: bytes.Repeat([]byte{0x51}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x52}, 128))}
	credential, err := manager.Issue()
	if err != nil {
		t.Fatal(err)
	}
	leaseExpiresAt := now.Add(4 * time.Minute)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'runtime-request@lites.invalid',$2,'en','active')`, []any{userID, now}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Runtime Request','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,due_at,profile_snapshot_id,budget_snapshot) VALUES($1,$2,$3,'31000000-0000-4000-8000-000000005199','waiting_tool',1,$4,'runtime-request@v1','{}')`, []any{runID, tenantID, userID, now.Add(time.Hour)}},
		{`INSERT INTO agent.outbox(id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at,published_at) VALUES($1,$2,$3,'ExecuteToolCall','tool_call',$4,$5,'encrypted://runtime-request/command','delivery-hash','published',$6,$6)`, []any{commandOutboxID, tenantID, commandID, toolID, epoch, now}},
		{`INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at) VALUES($1,$2,$3,'interactive','runtime-untrusted',100,1,5,'running',$4,$5,$4)`, []any{jobID, tenantID, commandID, now, now.Add(time.Hour)}},
		{`INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at) VALUES($1,$2,$3,$4,3,$5,$6,'tool-worker-5101','running',$7)`, []any{attemptID, tenantID, jobID, commandID, credential.Digest[:], leaseExpiresAt, now}},
		{`INSERT INTO agent.inbox(id,tenant_id,store_epoch,consumer_name,command_id,status,owner_attempt_id,fence,lease_token_hash,lease_expires_at,request_hash) VALUES($1,$2,$3,'tool-worker',$4,'running',$5,3,$6,$7,'delivery-hash')`, []any{inboxID, tenantID, epoch, commandID, attemptID, credential.Digest[:], leaseExpiresAt}},
		{`INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,active_command_id,active_attempt_id,current_fence,lease_token_hash,lease_expires_at,execution_mode) VALUES($1,$2,$3,$4,'executing',2,'code_execute','code_execute@1.0.0','encrypted://runtime-request/input',$5,'read_only',$6,$7,3,$8,$9,'worker_runtime')`, []any{toolID, tenantID, userID, runID, requestHash, commandID, attemptID, credential.Digest[:], leaseExpiresAt}},
	}
	for _, statement := range statements {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,1,'RuntimePolicySnapshotCreated',1,'runtime_policy',$4,1,$5,$6,$6,'{"kind":"system"}',$7,'encrypted://runtime-request/policy','policy-event')`, policyEventID, tenantID, userID, policyID, epoch, now, correlationID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.event_cursors(tenant_id,user_id,last_seq) VALUES($1,$2,1)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.runtime_policy_snapshots(id,tenant_id,snapshot_key,version,policy_hash,trust_tier,isolation_kind,network_mode,network_policy_hash,secret_mode,secret_scope_hash,workspace_mode,image_digest,kernel_digest,rootfs_digest,vcpu_count,memory_mib,disk_mib,pids_max,maximum_duration_seconds,idle_timeout_seconds,kill_grace_seconds,approval_required,policy_manifest,created_event_id,created_at) VALUES($1,$2,'runtime-policy:untrusted:v1',1,$3,'untrusted','firecracker','none',$4,'none',$4,'none',$5,$5,$5,2,512,4096,256,180,60,5,false,'{"schema_version":1}',$6,$7)`, policyID, tenantID, policyHash, empty, imageDigest, policyEventID, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	store := Store{Pool: agent, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, Epochs: fixedEpoch(epoch), StoreEpoch: epoch,
		IDKey: bytes.Repeat([]byte{0x53}, 32), NonceKey: bytes.Repeat([]byte{0x54}, 32), ExecutionTokens: manager, Now: func() time.Time { return now }}
	command := Command{Claim: executionpostgres.ToolClaim{ToolCallID: toolID, RunID: runID, TenantID: tenantID, UserID: userID, StoreEpoch: epoch,
		CommandID: commandID, ConsumerName: "tool-worker", RequestHash: "delivery-hash", InboxID: inboxID, AttemptID: attemptID, Fence: 3,
		LeaseToken: credential.Raw, LeaseExpiresAt: leaseExpiresAt, Binding: executionpostgres.ToolBinding{ToolName: "code_execute", DescriptorSnapshotID: "code_execute@1.0.0", RequestHash: requestHash}},
		Requirements: Requirements{PolicySnapshotID: policyID, PolicySnapshotKey: "runtime-policy:untrusted:v1", TrustTier: "untrusted", IsolationKind: "firecracker",
			ImageDigest: imageDigest, WorkspaceMode: runtimecontract.WorkspaceNone, NetworkMode: "none", NetworkPolicyHash: empty,
			SecretMode: "none", SecretScopeHash: empty, MinimumVCPU: 1, MinimumMemoryMiB: 256, MinimumDiskMiB: 1024, MinimumPids: 64, MaximumDuration: 2 * time.Minute},
		Payload: PayloadPointer{Ref: "encrypted://runtime-request/session", Hash: "session-event"}, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID}
	first, err := store.Request(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Request(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionID == "" || !second.Replayed || second.SessionID != first.SessionID || second.Nonce != first.Nonce || second.LeaseTokenHash != first.LeaseTokenHash {
		t.Fatalf("request/replay mismatch: first=%#v second=%#v", first, second)
	}
	var sessions, events, publishes int
	if err = admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.runtime_sessions WHERE tenant_id=$1 AND command_id=$2 AND execution_attempt_id=$3),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='runtime_session' AND aggregate_id=$4 AND event_type='RuntimeSessionRequested'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_kind='runtime_session' AND aggregate_id=$4 AND command_type='events.publish')`,
		tenantID, commandID, attemptID, first.SessionID).Scan(&sessions, &events, &publishes); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || events != 1 || publishes != 1 {
		t.Fatalf("durable counts = %d/%d/%d", sessions, events, publishes)
	}
	tampered := command
	tampered.Claim.LeaseToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err = store.Request(ctx, tampered); !errors.Is(err, ErrExecutionRight) {
		t.Fatalf("tampered execution lease error = %v", err)
	}
	weakened := command
	weakened.Requirements.NetworkMode = "broker_only"
	if _, err = store.Request(ctx, weakened); !errors.Is(err, ErrPolicyBinding) {
		t.Fatalf("policy weakening error = %v", err)
	}
}

func requestPool(t *testing.T, ctx context.Context, name string) *pgxpool.Pool {
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
