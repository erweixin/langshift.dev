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
	"github.com/langshift/lites/internal/payload"
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
	clock := now.Add(time.Second)
	store := Store{
		Pool: service, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }}, Epochs: fixedEpoch(epoch), StoreEpoch: epoch,
		IDKey: bytes.Repeat([]byte{0x65}, 32), TokenPepper: bytes.Repeat([]byte{0x66}, 32),
		Verifier: runtimecontract.CapabilityVerifier{Issuer: "event-service", Audience: "runtime-manager", Keys: map[string]ed25519.PublicKey{"runtime-key-v1": publicKey}, MaximumTTL: 5 * time.Minute},
		Now:      func() time.Time { return clock },
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
		TenantID: tenantID, CapabilityToken: capability, HostID: hostID, MachineID: "runtime-store-9201", GuestCID: 92,
		ProvisionLease: provisionLease, ProvisionLeaseExpiresAt: now.Add(time.Minute),
		Payload: PayloadPointer{Ref: "encrypted://runtime-store/provision", Hash: "runtime-store-provision"}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: "a0000000-0000-0000-0000-000000009201",
	}
	const staleApprovalID = "a0000000-0000-0000-0000-000000009299"
	if _, err = admin.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES('b0000000-0000-0000-0000-000000009299',$1,$2,100,'ApprovalGranted',1,'approval',$3,2,'90000000-0000-0000-0000-000000009299',$4,$4,'{"kind":"system"}',$5,'encrypted://runtime-store/stale-approval','runtime-store-stale-approval')`, tenantID, userID, staleApprovalID, now, command.CorrelationID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO agent.approvals(id,tenant_id,run_id,tool_call_id,version,approval_kind,proposal_hash,target_version,permission_snapshot,status,expires_at,requested_by,granted_at,created_at,updated_at) VALUES($1,$2,$3,$4,2,'tool_execution','runtime-stale-approval',1,'membership:stale:v1:role:owner','granted',$5,$6,$7,$7,$7)`, staleApprovalID, tenantID, runID, toolID, now.Add(time.Hour), userID, now); err != nil {
		t.Fatal(err)
	}
	unauthorizedClaims := claims
	unauthorizedClaims.ApprovalID = staleApprovalID
	unauthorizedClaims.ApprovalVersion = 2
	command.CapabilityToken, err = runtimecontract.SignCapability(unauthorizedClaims, "runtime-key-v1", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginProvision(ctx, command); !errors.Is(err, ErrCapabilityBinding) {
		t.Fatalf("cross-epoch approval = %v", err)
	}
	command.CapabilityToken = capability
	// A ToolWorker heartbeat may extend the same attempt/fence/token while the
	// RuntimeSession keeps its original immutable lease snapshot. The extended
	// authority remains valid; changing any identity field still fails below.
	extendedLease := now.Add(6 * time.Minute)
	if _, err = admin.Exec(ctx, `WITH tool_heartbeat AS (
		UPDATE agent.tool_calls SET lease_expires_at=$1 WHERE tenant_id=$2 AND id=$3 RETURNING id
	) UPDATE agent.job_attempts SET version=version+1,lease_expires_at=$1,updated_at=CURRENT_TIMESTAMP
	  WHERE tenant_id=$2 AND id=$4 AND EXISTS(SELECT 1 FROM tool_heartbeat)`, extendedLease, tenantID, toolID, attemptID); err != nil {
		t.Fatal(err)
	}
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

	clock = now.Add(2 * time.Second)
	readyCommand := ReadyCommand{LifecycleCommand: LifecycleCommand{
		TenantID: tenantID, SessionID: sessionID, ProvisionAttemptID: result.ProvisionAttemptID, ProvisionFence: 1, ExpectedVersion: 2,
		LeaseToken: provisionLease, ObservedAt: clock, Payload: PayloadPointer{Ref: "encrypted://runtime-store/ready", Hash: strings.Repeat("d", 64)}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: command.CorrelationID,
	}, BootReceiptHash: strings.Repeat("d", 64)}
	ready, err := store.MarkReady(ctx, readyCommand)
	if err != nil || ready.Status != "ready" || ready.Version != 3 || ready.Replayed {
		t.Fatalf("MarkReady() = %#v, %v", ready, err)
	}
	readyReplay, err := store.MarkReady(ctx, readyCommand)
	if err != nil || !readyReplay.Replayed || readyReplay.EventID != ready.EventID {
		t.Fatalf("replayed MarkReady() = %#v, %v", readyReplay, err)
	}
	wrongLease, err := store.IssueProvisionLease()
	if err != nil {
		t.Fatal(err)
	}
	clock = now.Add(3 * time.Second)
	runningCommand := LifecycleCommand{
		TenantID: tenantID, SessionID: sessionID, ProvisionAttemptID: result.ProvisionAttemptID, ProvisionFence: 1, ExpectedVersion: 3,
		LeaseToken: wrongLease, ObservedAt: clock, Payload: PayloadPointer{Ref: "encrypted://runtime-store/running", Hash: "runtime-store-running"}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: command.CorrelationID,
	}
	if _, err = store.MarkRunning(ctx, runningCommand); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("wrong lifecycle lease = %v", err)
	}
	runningCommand.LeaseToken = provisionLease
	running, err := store.MarkRunning(ctx, runningCommand)
	if err != nil || running.Status != "running" || running.Version != 4 {
		t.Fatalf("MarkRunning() = %#v, %v", running, err)
	}
	clock = now.Add(4 * time.Second)
	idleCommand := runningCommand
	idleCommand.ExpectedVersion, idleCommand.ObservedAt = 4, clock
	idleCommand.Payload = PayloadPointer{Ref: "encrypted://runtime-store/idle", Hash: "runtime-store-idle"}
	idle, err := store.MarkIdle(ctx, idleCommand)
	if err != nil || idle.Status != "idle" || idle.Version != 5 {
		t.Fatalf("MarkIdle() = %#v, %v", idle, err)
	}
	clock = now.Add(5 * time.Second)
	resumeCommand := runningCommand
	resumeCommand.ExpectedVersion, resumeCommand.ObservedAt = 5, clock
	resumeCommand.Payload = PayloadPointer{Ref: "encrypted://runtime-store/resumed", Hash: "runtime-store-resumed"}
	resumed, err := store.MarkRunning(ctx, resumeCommand)
	if err != nil || resumed.Status != "running" || resumed.Version != 6 {
		t.Fatalf("resumed MarkRunning() = %#v, %v", resumed, err)
	}
	clock = now.Add(6 * time.Second)
	recoveryIdentity := RecoveryIdentity{TenantID: tenantID, SessionID: sessionID, AllocationID: result.AllocationID, ProvisionAttemptID: result.ProvisionAttemptID, HostID: hostID, MachineID: result.MachineID, GuestCID: result.GuestCID}
	owned, err := store.InspectOwnedMachine(ctx, recoveryIdentity, controlHash)
	if err != nil || owned.SessionStatus != "running" || owned.SessionVersion != 6 || owned.AllocationStatus != "active" {
		t.Fatalf("InspectOwnedMachine() = %#v, %v", owned, err)
	}
	if _, err = store.InspectOwnedMachine(ctx, recoveryIdentity, bytes.Repeat([]byte{0xff}, 32)); err == nil {
		t.Fatal("wrong host control digest inspected owned machine")
	}
	inventory, err := store.ListHostMachines(ctx, hostID, controlHash, "", 100)
	if err != nil || len(inventory) != 1 || inventory[0].SessionID != sessionID {
		t.Fatalf("ListHostMachines() = %#v, %v", inventory, err)
	}
	recoveryCommand := RecoveryTerminationCommand{LifecycleCommand: LifecycleCommand{
		TenantID: tenantID, SessionID: sessionID, ExpectedVersion: 6,
		ObservedAt: clock, Payload: PayloadPointer{Ref: "encrypted://runtime-store/termination", Hash: "runtime-store-termination"}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: command.CorrelationID,
	}, Authority: RecoveryDeadline, Identity: recoveryIdentity, Reason: "execution_deadline"}
	if _, err = store.RequestRecoveryTermination(ctx, recoveryCommand); err == nil {
		t.Fatal("non-due runtime session was reclaimed by deadline sweeper")
	}
	const (
		cancellationID      = "ce920000-0000-4000-8000-000000000006"
		cancellationEventID = "be920000-0000-4000-8000-000000000006"
		cancellationOutbox  = "be920000-0000-4000-8000-000000000007"
		cancellationPublish = "be920000-0000-4000-8000-000000000008"
	)
	cancelTx, err := admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (eventpostgres.Appender{Now: func() time.Time { return clock }}).Append(ctx, cancelTx, eventpostgres.Input{Event: eventpostgres.Event{
		ID: cancellationEventID, TenantID: tenantID, UserID: userID, EventType: "RunCancellationRequested", SchemaVersion: 1,
		AggregateKind: "run_cancellation", AggregateID: cancellationID, AggregateVersion: 1, StoreEpoch: epoch,
		OccurredAt: clock, Actor: json.RawMessage(`{"kind":"user"}`), CorrelationID: command.CorrelationID,
		PayloadRef: "encrypted://runtime-store/run-cancellation", PayloadHash: strings.Repeat("7", 64),
	}, Commands: []eventpostgres.OutboxCommand{{ID: cancellationOutbox, CommandID: cancellationPublish, CommandType: "events.publish", PayloadRef: "encrypted://runtime-store/run-cancellation", PayloadHash: strings.Repeat("7", 64)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = cancelTx.Exec(ctx, `INSERT INTO agent.run_cancellations(
		id,tenant_id,run_id,root_cancellation_id,parent_cancellation_id,cancel_generation,status,version,requested_by,requested_at,reason,
		store_epoch,request_hash,request_event_id,request_payload_ref,request_payload_hash,settlement_payload_ref,settlement_payload_hash,reconciliation_due_at,created_at,updated_at
	) VALUES($1,$2,$3,$1,NULL,1,'terminating',2,$4,$5,'user_requested',$6,$7,$8,$9,$10,$11,$12,$5,$5,$5)`, cancellationID, tenantID, runID, userID, clock, epoch, strings.Repeat("8", 64), cancellationEventID, "encrypted://runtime-store/run-cancellation", strings.Repeat("7", 64), "encrypted://runtime-store/run-cancelled", strings.Repeat("9", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = cancelTx.Exec(ctx, `UPDATE agent.runs SET cancel_requested_at=$1,cancel_generation=1,active_cancellation_id=$2,current_fence=current_fence+1,updated_at=$1 WHERE tenant_id=$3 AND id=$4`, clock, cancellationID, tenantID, runID); err != nil {
		t.Fatal(err)
	}
	if err = cancelTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	payloads := &runtimeCancellationPayloadStore{values: map[string][]byte{}, manifests: map[string]payload.Manifest{}}
	converger := RunCancellationService{Store: store, Payloads: payloads}
	if err = converger.ConvergeRunCancellation(ctx, tenantID, runID, cancellationID, epoch); err != nil {
		t.Fatal(err)
	}
	if err = converger.ConvergeRunCancellation(ctx, tenantID, runID, cancellationID, epoch); err != nil {
		t.Fatalf("runtime cancellation replay: %v", err)
	}
	eventID, err := store.deterministicID("runtime-lifecycle-event:RuntimeTerminationRequested", sessionID, 7)
	if err != nil {
		t.Fatal(err)
	}
	manifest, ok := payloads.manifests[eventID]
	if !ok {
		t.Fatal("runtime cancellation evidence was not persisted")
	}
	recoveryCommand = RecoveryTerminationCommand{LifecycleCommand: LifecycleCommand{
		TenantID: tenantID, SessionID: sessionID, ExpectedVersion: 6, ObservedAt: clock,
		Payload: PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: command.CorrelationID,
	}, Authority: RecoveryCancellation, Identity: recoveryIdentity, RunID: runID, CancellationID: cancellationID, Reason: "run_cancelled"}
	terminationReplay, err := store.RequestRecoveryTermination(ctx, recoveryCommand)
	if err != nil || !terminationReplay.Replayed || terminationReplay.Version != 7 {
		t.Fatalf("replayed cancellation termination = %#v, %v", terminationReplay, err)
	}
	var sessionStatus, terminationReason string
	var terminationEvents int
	if err = admin.QueryRow(ctx, `SELECT s.status,s.termination_reason,
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='RuntimeTerminationRequested')
		FROM agent.runtime_sessions s WHERE s.tenant_id=$1 AND s.id=$2`, tenantID, sessionID).Scan(&sessionStatus, &terminationReason, &terminationEvents); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "termination_requested" || terminationReason != "run_cancelled" || terminationEvents != 1 {
		t.Fatalf("session=%s reason=%s termination_events=%d", sessionStatus, terminationReason, terminationEvents)
	}
	clock = now.Add(7 * time.Second)
	terminatedCommand := RecoveryTerminatedCommand{LifecycleCommand: LifecycleCommand{
		TenantID: tenantID, SessionID: sessionID, ExpectedVersion: terminationReplay.Version,
		ObservedAt: clock, Payload: PayloadPointer{Ref: "encrypted://runtime-store/terminated", Hash: "runtime-store-terminated"}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: command.CorrelationID,
	}, Identity: recoveryIdentity, HostControlHash: controlHash, CleanupReceiptHash: strings.Repeat("e", 64), UsageManifest: json.RawMessage(`{"schema_version":1,"vcpu_millis":10}`)}
	terminated, err := store.CompleteRecoveryTermination(ctx, terminatedCommand)
	if err != nil || terminated.Status != "terminated" || terminated.Version != 8 {
		t.Fatalf("CompleteRecoveryTermination() = %#v, %v", terminated, err)
	}
	terminatedReplay, err := store.CompleteRecoveryTermination(ctx, terminatedCommand)
	if err != nil || !terminatedReplay.Replayed || terminatedReplay.EventID != terminated.EventID {
		t.Fatalf("replayed CompleteRecoveryTermination() = %#v, %v", terminatedReplay, err)
	}
	if err = admin.QueryRow(ctx, `SELECT allocated_sessions FROM agent.runtime_hosts WHERE host_id=$1`, hostID).Scan(&allocatedSessions); err != nil || allocatedSessions != 0 {
		t.Fatalf("terminal capacity sessions=%d: %v", allocatedSessions, err)
	}
	const (
		hostlessToolID       = "40000000-0000-0000-0000-000000009211"
		hostlessCommandID    = "50000000-0000-0000-0000-000000009211"
		hostlessOutboxID     = "50000000-0000-0000-0000-000000009212"
		hostlessJobID        = "50000000-0000-0000-0000-000000009213"
		hostlessAttemptID    = "60000000-0000-0000-0000-000000009211"
		hostlessSessionID    = "80000000-0000-0000-0000-000000009211"
		hostlessEventID      = "b0000000-0000-0000-0000-000000009211"
		hostlessEventOutbox  = "b0000000-0000-0000-0000-000000009212"
		hostlessEventPublish = "b0000000-0000-0000-0000-000000009213"
	)
	clock = now.Add(8 * time.Second)
	hostlessLease := bytes.Repeat([]byte{0x71}, 32)
	hostlessNonce := bytes.Repeat([]byte{0x72}, 32)
	hostlessRequest := strings.Repeat("f", 64)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO agent.outbox(id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at,published_at) VALUES($1,$2,$3,'ExecuteToolCall','tool_call',$4,$5,'encrypted://runtime-hostless/command','runtime-hostless-command','published',$6,$6)`, []any{hostlessOutboxID, tenantID, hostlessCommandID, hostlessToolID, epoch, clock}},
		{`INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at) VALUES($1,$2,$3,'interactive','runtime-untrusted',100,1,5,'running',$4,$5,$4)`, []any{hostlessJobID, tenantID, hostlessCommandID, clock, now.Add(time.Hour)}},
		{`INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at) VALUES($1,$2,$3,$4,1,$5,$6,'runtime-worker-hostless','running',$7)`, []any{hostlessAttemptID, tenantID, hostlessJobID, hostlessCommandID, hostlessLease, now.Add(5 * time.Minute), clock}},
		{`INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,effect_key,active_command_id,active_attempt_id,current_fence,lease_token_hash,lease_expires_at) VALUES($1,$2,$3,$4,'executing',1,'code_execute','code_execute@v1','encrypted://runtime-hostless/input',$5,'idempotent_write','runtime-hostless-effect',$6,$7,1,$8,$9)`, []any{hostlessToolID, tenantID, userID, runID, hostlessRequest, hostlessCommandID, hostlessAttemptID, hostlessLease, now.Add(5 * time.Minute)}},
	} {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	tx, err = admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = (eventpostgres.Appender{Now: func() time.Time { return clock }}).Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{
		ID: hostlessEventID, TenantID: tenantID, UserID: userID, EventType: "RuntimeSessionRequested", SchemaVersion: 1,
		AggregateKind: "runtime_session", AggregateID: hostlessSessionID, AggregateVersion: 1, StoreEpoch: epoch,
		OccurredAt: clock, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: command.CorrelationID,
		PayloadRef: "encrypted://runtime-hostless/requested", PayloadHash: "runtime-hostless-requested",
	}, Commands: []eventpostgres.OutboxCommand{{ID: hostlessEventOutbox, CommandID: hostlessEventPublish, CommandType: "events.publish", PayloadRef: "encrypted://runtime-hostless/requested", PayloadHash: "runtime-hostless-requested"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.runtime_sessions(id,tenant_id,user_id,run_id,tool_call_id,version,status,policy_snapshot_id,policy_snapshot_key,policy_hash,trust_tier,isolation_kind,workspace_mode,network_policy_hash,secret_scope_hash,request_hash,capability_nonce_hash,command_id,execution_attempt_id,execution_fence,execution_lease_hash,execution_lease_expires_at,requested_at,execution_deadline,created_event_id,last_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,1,'requested',$6,'runtime-policy:untrusted:v1',$7,'untrusted','firecracker','none',$8,$8,$9,$10,$11,$12,1,$13,$14,$15,$16,$17,$17,$15,$15)`, hostlessSessionID, tenantID, userID, runID, hostlessToolID, policyID, policyHash, empty, hostlessRequest, hostlessNonce, hostlessCommandID, hostlessAttemptID, hostlessLease, now.Add(5*time.Minute), clock, now.Add(10*time.Minute), hostlessEventID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(9 * time.Second)
	if err = converger.ConvergeRunCancellation(ctx, tenantID, runID, cancellationID, epoch); err != nil {
		t.Fatalf("hostless runtime cancellation: %v", err)
	}
	hostlessTerminationEventID, err := store.deterministicID("runtime-lifecycle-event:RuntimeTerminationRequested", hostlessSessionID, 2)
	if err != nil {
		t.Fatal(err)
	}
	hostlessManifest, ok := payloads.manifests[hostlessTerminationEventID]
	if !ok {
		t.Fatal("hostless runtime cancellation evidence was not persisted")
	}
	hostlessTermination := TerminationCommand{LifecycleCommand: LifecycleCommand{
		TenantID: tenantID, SessionID: hostlessSessionID, ExpectedVersion: 1, ObservedAt: clock,
		Payload: PayloadPointer{Ref: hostlessManifest.Ref, Hash: hostlessManifest.Hash}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: command.CorrelationID,
	}, Reason: "run_cancelled"}
	if result, requestErr := store.RequestTermination(ctx, hostlessTermination); requestErr != nil || result.Version != 2 || !result.Replayed {
		t.Fatalf("replayed hostless cancellation = %#v, %v", result, requestErr)
	}
	clock = now.Add(10 * time.Second)
	hostlessTerminated := TerminatedCommand{LifecycleCommand: LifecycleCommand{
		TenantID: tenantID, SessionID: hostlessSessionID, ExpectedVersion: 2, ObservedAt: clock,
		Payload: PayloadPointer{Ref: "encrypted://runtime-hostless/terminated", Hash: "runtime-hostless-terminated"}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: command.CorrelationID,
	}, CleanupReceiptHash: strings.Repeat("a", 64), UsageManifest: json.RawMessage(`{"schema_version":1,"vcpu_millis":0}`)}
	if result, completeErr := store.CompleteTermination(ctx, hostlessTerminated); completeErr != nil || result.Version != 3 {
		t.Fatalf("hostless CompleteTermination() = %#v, %v", result, completeErr)
	}
	var hostlessAllocations int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.runtime_allocations WHERE tenant_id=$1 AND session_id=$2`, tenantID, hostlessSessionID).Scan(&hostlessAllocations); err != nil || hostlessAllocations != 0 {
		t.Fatalf("hostless allocations=%d: %v", hostlessAllocations, err)
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

type runtimeCancellationPayloadStore struct {
	values    map[string][]byte
	manifests map[string]payload.Manifest
}

func (store *runtimeCancellationPayloadStore) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	digest := sha256.Sum256(value)
	manifest := payload.Manifest{Ref: "memory://runtime-cancellation/" + descriptor.ObjectID, Hash: hex.EncodeToString(digest[:]), KeyID: "test", AADHash: strings.Repeat("a", 64)}
	store.values[manifest.Ref] = append([]byte(nil), value...)
	store.manifests[descriptor.ObjectID] = manifest
	return manifest, nil
}

func (store *runtimeCancellationPayloadStore) Get(_ context.Context, _ payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	value, ok := store.values[manifest.Ref]
	if !ok {
		return nil, payload.ErrIntegrity
	}
	return append([]byte(nil), value...), nil
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
