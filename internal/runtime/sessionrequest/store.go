// Package sessionrequest creates the durable, fenced RuntimeSession authority
// used by ToolWorker before any sandbox host may reserve capacity.
package sessionrequest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/platform/ids"
	runtimecontract "github.com/langshift/lites/internal/runtime"
	"github.com/langshift/lites/internal/security/opaque"
)

var (
	ErrConfiguration  = errors.New("runtime session requester configuration is invalid")
	ErrCommand        = errors.New("runtime session request is invalid")
	ErrExecutionRight = errors.New("runtime session request lost its tool execution right")
	ErrPolicyBinding  = errors.New("runtime policy does not satisfy the immutable tool requirements")
	ErrConflict       = errors.New("runtime session request conflicts with durable state")
	ErrStaleEpoch     = errors.New("runtime session requester store epoch is stale")
)

var hexHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

type PayloadPointer struct {
	Ref  string
	Hash string
}

// Requirements are derived from the already content-pinned tool descriptor.
// The caller chooses an exact reviewed policy snapshot, while this store
// rejects a snapshot that weakens any immutable descriptor requirement.
type Requirements struct {
	PolicySnapshotID, PolicySnapshotKey                        string
	TrustTier, IsolationKind, ImageDigest                      string
	WorkspaceMode                                              runtimecontract.WorkspaceMode
	NetworkMode, NetworkPolicyHash                             string
	SecretMode, SecretScopeHash                                string
	MinimumVCPU, MinimumMemoryMiB, MinimumDiskMiB, MinimumPids int
	MaximumDuration                                            time.Duration
	ApprovalRequired                                           bool
}

type Command struct {
	Claim                              executionpostgres.ToolClaim
	Requirements                       Requirements
	WorkspaceID, BaseWorkspaceRevision string
	ApprovalID                         string
	ApprovalVersion                    uint64
	Payload                            PayloadPointer
	Actor                              json.RawMessage
	CorrelationID                      string
}

type Requested struct {
	SessionID, TenantID, UserID, RunID, ToolCallID          string
	CommandID, AttemptID                                    string
	Fence                                                   uint64
	PolicySnapshotID, PolicySnapshotKey, PolicyHash         string
	WorkspaceID, BaseWorkspaceRevision                      string
	WorkspaceMode                                           runtimecontract.WorkspaceMode
	NetworkPolicyHash, SecretScopeHash, RequestHash         string
	ApprovalID                                              string
	ApprovalVersion                                         uint64
	LeaseTokenHash, Nonce                                   string
	RequestedAt, ExecutionLeaseExpiresAt, ExecutionDeadline time.Time
	Status                                                  string
	Replayed                                                bool
}

type Store struct {
	Pool            *pgxpool.Pool
	Appender        eventpostgres.Appender
	Epochs          EpochAuthority
	StoreEpoch      string
	IDKey, NonceKey []byte
	ExecutionTokens opaque.Manager
	Now             func() time.Time
}

type policyRow struct {
	ID, SnapshotKey, PolicyHash                              string
	TrustTier, IsolationKind, NetworkMode, NetworkPolicyHash string
	SecretMode, SecretScopeHash, WorkspaceMode, ImageDigest  string
	VCPU, MemoryMiB, DiskMiB, Pids, MaximumSeconds           int
	ApprovalRequired                                         bool
}

func (store Store) Request(ctx context.Context, command Command) (Requested, error) {
	if !store.valid() {
		return Requested{}, ErrConfiguration
	}
	if !validCommand(command) {
		return Requested{}, ErrCommand
	}
	epoch, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch == "" {
		return Requested{}, ErrConfiguration
	}
	if epoch != store.StoreEpoch || command.Claim.StoreEpoch != store.StoreEpoch {
		return Requested{}, ErrStaleEpoch
	}
	leaseDigest, err := store.ExecutionTokens.Digest(command.Claim.LeaseToken)
	if err != nil {
		return Requested{}, ErrCommand
	}
	identifiers, err := store.identifiers(command.Claim)
	if err != nil {
		return Requested{}, ErrConfiguration
	}
	nonce := store.nonce(identifiers.session)
	nonceDigest, err := runtimecontract.CapabilityNonceDigest(nonce)
	if err != nil {
		return Requested{}, ErrConfiguration
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if store.Now != nil {
		now = store.Now().UTC().Truncate(time.Microsecond)
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Requested{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.Claim.TenantID); err != nil {
		return Requested{}, err
	}
	var runDueAt time.Time
	err = tx.QueryRow(ctx, `SELECT r.due_at
		FROM agent.tool_calls t
		JOIN agent.runs r ON r.tenant_id=t.tenant_id AND r.id=t.run_id
		JOIN agent.inbox i ON i.tenant_id=t.tenant_id AND i.id=$1
		JOIN agent.job_attempts a ON a.tenant_id=t.tenant_id AND a.id=$2
		WHERE t.tenant_id=$3 AND t.id=$4 AND t.user_id=$5 AND t.run_id=$6
		  AND t.status='executing' AND t.execution_mode='worker_runtime'
		  AND t.active_command_id=$7 AND t.active_attempt_id=$2 AND t.current_fence=$8
		  AND t.lease_token_hash=$9 AND t.lease_expires_at=$10 AND t.lease_expires_at>$11
		  AND t.request_hash=$12 AND t.descriptor_snapshot_id=$13 AND t.tool_name=$14
		  AND i.store_epoch=$15 AND i.consumer_name=$16 AND i.command_id=$7
		  AND i.status='running' AND i.owner_attempt_id=$2 AND i.fence=$8
		  AND i.lease_token_hash=$9 AND i.lease_expires_at=$10 AND i.request_hash=$17
		  AND a.command_id=$7 AND a.status='running' AND a.fence=$8
		  AND a.lease_token_hash=$9 AND a.lease_expires_at=$10
		  AND r.status='waiting_tool' AND r.cancel_requested_at IS NULL AND r.due_at>$11
		FOR UPDATE OF t,r,i,a`, command.Claim.InboxID, command.Claim.AttemptID, command.Claim.TenantID,
		command.Claim.ToolCallID, command.Claim.UserID, command.Claim.RunID, command.Claim.CommandID,
		command.Claim.Fence, leaseDigest[:], command.Claim.LeaseExpiresAt.UTC().Truncate(time.Microsecond), now,
		command.Claim.Binding.RequestHash, command.Claim.Binding.DescriptorSnapshotID, command.Claim.Binding.ToolName,
		command.Claim.StoreEpoch, command.Claim.ConsumerName, command.Claim.RequestHash).Scan(&runDueAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Requested{}, ErrExecutionRight
	}
	if err != nil {
		return Requested{}, err
	}
	policy, err := loadPolicy(ctx, tx, command.Claim.TenantID, command.Requirements)
	if err != nil {
		return Requested{}, err
	}
	if !policySatisfies(policy, command.Requirements) || policy.ApprovalRequired && command.ApprovalID == "" {
		return Requested{}, ErrPolicyBinding
	}
	deadline := minimumTime(now.Add(command.Requirements.MaximumDuration), now.Add(time.Duration(policy.MaximumSeconds)*time.Second), command.Claim.LeaseExpiresAt, runDueAt).UTC().Truncate(time.Microsecond)
	if !deadline.After(now.Add(time.Second)) {
		return Requested{}, ErrExecutionRight
	}
	requested := buildRequested(command, policy, identifiers.session, nonce, base64.RawURLEncoding.EncodeToString(leaseDigest[:]), now, deadline)
	requested.ExecutionLeaseExpiresAt = command.Claim.LeaseExpiresAt.UTC().Truncate(time.Microsecond)
	replay, replayErr := loadReplay(ctx, tx, requested, nonceDigest[:], leaseDigest[:])
	if replayErr == nil {
		replay.Replayed = true
		return replay, tx.Commit(ctx)
	}
	if !errors.Is(replayErr, pgx.ErrNoRows) {
		return Requested{}, replayErr
	}
	causation := command.Claim.CommandID
	if _, err = store.Appender.Append(ctx, tx, eventpostgres.Input{
		Event: eventpostgres.Event{ID: identifiers.event, TenantID: requested.TenantID, UserID: requested.UserID,
			EventType: "RuntimeSessionRequested", SchemaVersion: 1, AggregateKind: "runtime_session",
			AggregateID: requested.SessionID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch,
			OccurredAt: now, Actor: command.Actor, CausationID: &causation, CorrelationID: command.CorrelationID,
			PayloadRef: command.Payload.Ref, PayloadHash: command.Payload.Hash},
		Commands: []eventpostgres.OutboxCommand{{ID: identifiers.outbox, CommandID: identifiers.publish,
			CommandType: "events.publish", PayloadRef: command.Payload.Ref, PayloadHash: command.Payload.Hash}},
	}); err != nil {
		return Requested{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent.runtime_sessions(
		id,tenant_id,user_id,run_id,tool_call_id,version,status,
		policy_snapshot_id,policy_snapshot_key,policy_hash,trust_tier,isolation_kind,
		workspace_id,base_workspace_revision,workspace_mode,network_policy_hash,secret_scope_hash,request_hash,
		capability_nonce_hash,command_id,execution_attempt_id,execution_fence,execution_lease_hash,execution_lease_expires_at,
		approval_id,approval_version,requested_at,execution_deadline,created_event_id,last_event_id,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,1,'requested',$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,NULLIF($12,''),$13,$14,$15,$16,
		$17,$18,$19,$20,$21,$22,NULLIF($23,'')::uuid,NULLIF($24,0),$25,$26,$27,$27,$25,$25)`,
		requested.SessionID, requested.TenantID, requested.UserID, requested.RunID, requested.ToolCallID,
		requested.PolicySnapshotID, requested.PolicySnapshotKey, requested.PolicyHash, policy.TrustTier, policy.IsolationKind,
		requested.WorkspaceID, requested.BaseWorkspaceRevision, requested.WorkspaceMode, requested.NetworkPolicyHash,
		requested.SecretScopeHash, requested.RequestHash, nonceDigest[:], requested.CommandID, requested.AttemptID,
		requested.Fence, leaseDigest[:], requested.ExecutionLeaseExpiresAt, requested.ApprovalID, requested.ApprovalVersion,
		requested.RequestedAt, requested.ExecutionDeadline, identifiers.event)
	if err != nil {
		return Requested{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Requested{}, err
	}
	return requested, nil
}

type requestIDs struct{ session, event, outbox, publish string }

func (store Store) identifiers(claim executionpostgres.ToolClaim) (requestIDs, error) {
	seed := claim.TenantID + "\x00" + claim.CommandID + "\x00" + claim.AttemptID
	values := requestIDs{}
	pairs := []struct {
		domain string
		target *string
	}{
		{"runtime-session", &values.session}, {"runtime-session-requested-event", &values.event},
		{"runtime-session-requested-outbox", &values.outbox}, {"runtime-session-requested-publish", &values.publish},
	}
	for _, pair := range pairs {
		value, err := ids.DeterministicUUID(store.IDKey, pair.domain, seed)
		if err != nil {
			return requestIDs{}, err
		}
		*pair.target = value
	}
	return values, nil
}

func (store Store) nonce(sessionID string) string {
	mac := hmac.New(sha256.New, store.NonceKey)
	_, _ = mac.Write([]byte("runtime-capability-nonce-v1\x00" + sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (store Store) valid() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32 && len(store.NonceKey) >= 32
}

func validCommand(command Command) bool {
	claim, requirement := command.Claim, command.Requirements
	if claim.ToolCallID == "" || claim.RunID == "" || claim.TenantID == "" || claim.UserID == "" || claim.StoreEpoch == "" ||
		claim.CommandID == "" || claim.ConsumerName == "" || claim.RequestHash == "" || claim.InboxID == "" || claim.AttemptID == "" ||
		claim.Fence == 0 || claim.LeaseToken == "" || claim.LeaseExpiresAt.IsZero() || claim.Binding.ToolName == "" ||
		claim.Binding.DescriptorSnapshotID == "" || !hexHashPattern.MatchString(claim.Binding.RequestHash) ||
		requirement.PolicySnapshotID == "" || requirement.PolicySnapshotKey == "" || requirement.TrustTier == "" ||
		requirement.IsolationKind == "" || !digestPattern.MatchString(requirement.ImageDigest) ||
		!digestPattern.MatchString(requirement.NetworkPolicyHash) || !digestPattern.MatchString(requirement.SecretScopeHash) ||
		requirement.MinimumVCPU < 1 || requirement.MinimumMemoryMiB < 1 || requirement.MinimumDiskMiB < 0 || requirement.MinimumPids < 1 ||
		requirement.MaximumDuration < time.Second || requirement.MaximumDuration > time.Hour ||
		(command.ApprovalID == "") != (command.ApprovalVersion == 0) || requirement.ApprovalRequired && command.ApprovalID == "" ||
		command.Payload.Ref == "" || command.Payload.Hash == "" || command.CorrelationID == "" || !jsonObject(command.Actor) {
		return false
	}
	if requirement.WorkspaceMode != runtimecontract.WorkspaceNone && requirement.WorkspaceMode != runtimecontract.WorkspaceReadOnly && requirement.WorkspaceMode != runtimecontract.WorkspaceReadWrite {
		return false
	}
	return requirement.WorkspaceMode == runtimecontract.WorkspaceNone && command.WorkspaceID == "" && command.BaseWorkspaceRevision == "" ||
		requirement.WorkspaceMode != runtimecontract.WorkspaceNone && command.WorkspaceID != "" && command.BaseWorkspaceRevision != ""
}

func jsonObject(value json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func loadPolicy(ctx context.Context, tx pgx.Tx, tenantID string, requirement Requirements) (policyRow, error) {
	var policy policyRow
	err := tx.QueryRow(ctx, `SELECT id::text,snapshot_key,policy_hash,trust_tier,isolation_kind,network_mode,network_policy_hash,
		secret_mode,secret_scope_hash,workspace_mode,image_digest,vcpu_count,memory_mib,disk_mib,pids_max,
		maximum_duration_seconds,approval_required
		FROM agent.runtime_policy_snapshots WHERE tenant_id=$1 AND id=$2 AND snapshot_key=$3`,
		tenantID, requirement.PolicySnapshotID, requirement.PolicySnapshotKey).Scan(
		&policy.ID, &policy.SnapshotKey, &policy.PolicyHash, &policy.TrustTier, &policy.IsolationKind,
		&policy.NetworkMode, &policy.NetworkPolicyHash, &policy.SecretMode, &policy.SecretScopeHash,
		&policy.WorkspaceMode, &policy.ImageDigest, &policy.VCPU, &policy.MemoryMiB, &policy.DiskMiB,
		&policy.Pids, &policy.MaximumSeconds, &policy.ApprovalRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return policyRow{}, ErrPolicyBinding
	}
	return policy, err
}

func policySatisfies(policy policyRow, requirement Requirements) bool {
	return policy.ID == requirement.PolicySnapshotID && policy.SnapshotKey == requirement.PolicySnapshotKey &&
		hexHashPattern.MatchString(policy.PolicyHash) && policy.TrustTier == requirement.TrustTier &&
		policy.IsolationKind == requirement.IsolationKind && policy.ImageDigest == requirement.ImageDigest &&
		policy.WorkspaceMode == string(requirement.WorkspaceMode) && policy.NetworkMode == requirement.NetworkMode &&
		policy.NetworkPolicyHash == requirement.NetworkPolicyHash && policy.SecretMode == requirement.SecretMode &&
		policy.SecretScopeHash == requirement.SecretScopeHash && policy.VCPU >= requirement.MinimumVCPU &&
		policy.MemoryMiB >= requirement.MinimumMemoryMiB && policy.DiskMiB >= requirement.MinimumDiskMiB &&
		policy.Pids >= requirement.MinimumPids && policy.MaximumSeconds > 0 &&
		(!requirement.ApprovalRequired || policy.ApprovalRequired)
}

func buildRequested(command Command, policy policyRow, sessionID, nonce, leaseHash string, requestedAt, deadline time.Time) Requested {
	return Requested{SessionID: sessionID, TenantID: command.Claim.TenantID, UserID: command.Claim.UserID,
		RunID: command.Claim.RunID, ToolCallID: command.Claim.ToolCallID, CommandID: command.Claim.CommandID,
		AttemptID: command.Claim.AttemptID, Fence: command.Claim.Fence, PolicySnapshotID: policy.ID,
		PolicySnapshotKey: policy.SnapshotKey, PolicyHash: policy.PolicyHash, WorkspaceID: command.WorkspaceID,
		BaseWorkspaceRevision: command.BaseWorkspaceRevision, WorkspaceMode: command.Requirements.WorkspaceMode,
		NetworkPolicyHash: policy.NetworkPolicyHash, SecretScopeHash: policy.SecretScopeHash,
		RequestHash: command.Claim.Binding.RequestHash, ApprovalID: command.ApprovalID, ApprovalVersion: command.ApprovalVersion,
		LeaseTokenHash: leaseHash, Nonce: nonce, RequestedAt: requestedAt, ExecutionDeadline: deadline, Status: "requested"}
}

func loadReplay(ctx context.Context, tx pgx.Tx, expected Requested, nonceHash, leaseHash []byte) (Requested, error) {
	var actual Requested
	var actualNonce, actualLease []byte
	var workspaceID, baseRevision, approvalID sql.NullString
	var approvalVersion sql.NullInt64
	err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,user_id::text,run_id::text,tool_call_id::text,status,
		policy_snapshot_id::text,policy_snapshot_key,policy_hash,workspace_id::text,base_workspace_revision,workspace_mode,
		network_policy_hash,secret_scope_hash,request_hash,capability_nonce_hash,command_id::text,execution_attempt_id::text,
		execution_fence,execution_lease_hash,execution_lease_expires_at,approval_id::text,approval_version,requested_at,execution_deadline
		FROM agent.runtime_sessions WHERE tenant_id=$1 AND command_id=$2 AND execution_attempt_id=$3`,
		expected.TenantID, expected.CommandID, expected.AttemptID).Scan(&actual.SessionID, &actual.TenantID, &actual.UserID,
		&actual.RunID, &actual.ToolCallID, &actual.Status, &actual.PolicySnapshotID, &actual.PolicySnapshotKey, &actual.PolicyHash,
		&workspaceID, &baseRevision, &actual.WorkspaceMode, &actual.NetworkPolicyHash, &actual.SecretScopeHash,
		&actual.RequestHash, &actualNonce, &actual.CommandID, &actual.AttemptID, &actual.Fence, &actualLease,
		&actual.ExecutionLeaseExpiresAt, &approvalID, &approvalVersion, &actual.RequestedAt, &actual.ExecutionDeadline)
	if err != nil {
		return Requested{}, err
	}
	if workspaceID.Valid {
		actual.WorkspaceID = workspaceID.String
	}
	if baseRevision.Valid {
		actual.BaseWorkspaceRevision = baseRevision.String
	}
	if approvalID.Valid {
		actual.ApprovalID = approvalID.String
	}
	if approvalVersion.Valid && approvalVersion.Int64 > 0 {
		actual.ApprovalVersion = uint64(approvalVersion.Int64)
	}
	actual.LeaseTokenHash, actual.Nonce = expected.LeaseTokenHash, expected.Nonce
	if actual.SessionID != expected.SessionID || actual.TenantID != expected.TenantID || actual.UserID != expected.UserID ||
		actual.RunID != expected.RunID || actual.ToolCallID != expected.ToolCallID || actual.PolicySnapshotID != expected.PolicySnapshotID ||
		actual.PolicySnapshotKey != expected.PolicySnapshotKey || actual.PolicyHash != expected.PolicyHash ||
		actual.WorkspaceID != expected.WorkspaceID || actual.BaseWorkspaceRevision != expected.BaseWorkspaceRevision ||
		actual.WorkspaceMode != expected.WorkspaceMode || actual.NetworkPolicyHash != expected.NetworkPolicyHash ||
		actual.SecretScopeHash != expected.SecretScopeHash || actual.RequestHash != expected.RequestHash ||
		actual.CommandID != expected.CommandID || actual.AttemptID != expected.AttemptID || actual.Fence != expected.Fence ||
		actual.ApprovalID != expected.ApprovalID || actual.ApprovalVersion != expected.ApprovalVersion ||
		!bytes.Equal(actualNonce, nonceHash) || !bytes.Equal(actualLease, leaseHash) {
		return Requested{}, ErrConflict
	}
	return actual, nil
}

func minimumTime(values ...time.Time) time.Time {
	minimum := values[0]
	for _, value := range values[1:] {
		if value.Before(minimum) {
			minimum = value
		}
	}
	return minimum
}
