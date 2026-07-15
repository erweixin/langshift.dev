package toolworker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/payload"
	runtimecontract "github.com/langshift/lites/internal/runtime"
	runtimeclient "github.com/langshift/lites/internal/runtime/client"
	"github.com/langshift/lites/internal/runtime/firecracker"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
	"github.com/langshift/lites/internal/runtime/sessionrequest"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/langshift/lites/internal/toolregistry"
)

var (
	ErrSandboxAuthority = errors.New("sandbox authority does not match the immutable tool descriptor")
	ErrSandboxCapacity  = errors.New("no compatible runtime host has healthy capacity")
)

type RuntimeSessionIssuer interface {
	Request(context.Context, sessionrequest.Command) (sessionrequest.Issued, error)
}

// SandboxHostEndpoint is release inventory, not service discovery. The
// kernel/rootfs pins prevent a healthy host in the right trust pool from being
// selected for a policy it cannot boot.
type SandboxHostEndpoint struct {
	URL          string `json:"url"`
	KernelDigest string `json:"kernel_digest"`
	RootFSDigest string `json:"rootfs_digest"`
}

type PostgresSandboxBroker struct {
	Pool                        *pgxpool.Pool
	PlacementPool               *pgxpool.Pool
	Issuer                      RuntimeSessionIssuer
	Payloads                    payload.Store
	Endpoints                   map[string]SandboxHostEndpoint
	HTTPClient                  *http.Client
	AllowInsecureDevelopment    bool
	ProvisionTokenPepper        []byte
	ProvisionTokenDerivationKey []byte
	MachineIdentityKey          []byte
	Actor                       json.RawMessage
	ScratchRate                 firecracker.RateLimit
	Now                         func() time.Time
}

type selectedRuntimePolicy struct {
	ID, SnapshotKey, PolicyHash                              string
	TrustTier, IsolationKind, NetworkMode, NetworkPolicyHash string
	SecretMode, SecretScopeHash, WorkspaceMode, ImageDigest  string
	KernelDigest, RootFSDigest                               string
	VCPU, MemoryMiB, DiskMiB, Pids, MaximumSeconds           int
	ApprovalRequired                                         bool
	ApprovalID                                               string
	ApprovalVersion                                          uint64
}

type persistedSandboxSession struct {
	Status, HostID, MachineID string
	GuestCID                  uint32
	ProvisionLeaseExpiresAt   *time.Time
}

func (broker PostgresSandboxBroker) Acquire(ctx context.Context, request SandboxAcquireRequest) (SandboxSession, error) {
	if !broker.valid() || !validSandboxAcquireRequest(request) {
		return SandboxSession{}, ErrSandboxConfiguration
	}
	policy, err := broker.selectPolicy(ctx, request)
	if err != nil {
		return SandboxSession{}, err
	}
	requestedEvidence, err := broker.putEvidence(ctx, request.Execution.Claim.TenantID, request.Execution.Claim.AttemptID+"-runtime-request", "RuntimeSessionRequested", map[string]any{
		"tool_call_id": request.Execution.Claim.ToolCallID, "attempt_id": request.Execution.Claim.AttemptID,
		"descriptor_snapshot_id": request.Snapshot.SnapshotID, "descriptor_hash": request.Snapshot.Hash,
		"runtime_policy_snapshot_key": policy.SnapshotKey, "request_id": request.RequestID,
	})
	if err != nil {
		return SandboxSession{}, err
	}
	timeout, _ := time.ParseDuration(request.Snapshot.Descriptor.Resources.Timeout)
	issued, err := broker.Issuer.Request(ctx, sessionrequest.Command{
		Claim: request.Execution.Claim,
		Requirements: sessionrequest.Requirements{
			PolicySnapshotID: policy.ID, PolicySnapshotKey: policy.SnapshotKey, TrustTier: policy.TrustTier,
			IsolationKind: policy.IsolationKind, ImageDigest: policy.ImageDigest,
			WorkspaceMode: runtimecontract.WorkspaceMode(policy.WorkspaceMode), NetworkMode: policy.NetworkMode,
			NetworkPolicyHash: policy.NetworkPolicyHash, SecretMode: policy.SecretMode, SecretScopeHash: policy.SecretScopeHash,
			MinimumVCPU: minimumVCPU(request.Snapshot.Descriptor.Resources.CPUMillis), MinimumMemoryMiB: bytesToMiBRoundUp(request.Snapshot.Descriptor.Resources.MemoryBytes),
			MinimumDiskMiB: bytesToMiBRoundUp(request.Snapshot.Descriptor.Resources.DiskBytes), MinimumPids: request.Snapshot.Descriptor.RuntimePolicy.MinimumPids,
			MaximumDuration: timeout, ApprovalRequired: policy.ApprovalRequired,
		},
		ApprovalID: policy.ApprovalID, ApprovalVersion: policy.ApprovalVersion,
		Payload: sessionrequest.PayloadPointer{Ref: requestedEvidence.Ref, Hash: requestedEvidence.Hash},
		Actor:   broker.Actor, CorrelationID: request.Execution.Payload.CorrelationID,
	})
	if err != nil {
		return SandboxSession{}, err
	}
	state, err := broker.loadSession(ctx, issued)
	if err != nil {
		return SandboxSession{}, err
	}
	lease := broker.provisionLease(issued.SessionID)
	if lease == "" {
		return SandboxSession{}, ErrSandboxConfiguration
	}
	if state.Status == "ready" || state.Status == "running" || state.Status == "idle" {
		host, hostErr := broker.host(state.HostID, policy)
		if hostErr != nil {
			return SandboxSession{}, hostErr
		}
		return SandboxSession{TenantID: issued.TenantID, SessionID: issued.SessionID, CapabilityToken: issued.CapabilityToken, ProvisionLease: lease, Host: host}, nil
	}
	if state.Status != "requested" && state.Status != "provisioning" {
		return SandboxSession{}, ErrSandboxAuthority
	}
	hostID, machineID, guestCID := state.HostID, state.MachineID, state.GuestCID
	leaseExpiresAt := broker.provisionExpiry(issued)
	if state.Status == "provisioning" {
		if state.ProvisionLeaseExpiresAt == nil {
			return SandboxSession{}, ErrSandboxAuthority
		}
		leaseExpiresAt = *state.ProvisionLeaseExpiresAt
	} else {
		hostID, err = broker.place(ctx, policy)
		if err != nil {
			return SandboxSession{}, err
		}
		machineID, guestCID = broker.machineIdentity(issued.SessionID, hostID)
	}
	if !leaseExpiresAt.After(broker.now()) || machineID == "" || guestCID < 3 {
		return SandboxSession{}, ErrSandboxAuthority
	}
	host, err := broker.host(hostID, policy)
	if err != nil {
		return SandboxSession{}, err
	}
	provisionEvidence, err := broker.putEvidence(ctx, issued.TenantID, issued.SessionID+"-provision", "RuntimeSessionProvisioningStarted", map[string]any{
		"session_id": issued.SessionID, "host_id": hostID, "machine_id": machineID, "guest_cid": guestCID,
		"runtime_policy_snapshot_key": policy.SnapshotKey, "runtime_policy_hash": policy.PolicyHash,
	})
	if err != nil {
		return SandboxSession{}, err
	}
	provisioned, err := host.Provision(ctx, runtimeclient.ProvisionRequest{
		TenantID: issued.TenantID, CapabilityToken: issued.CapabilityToken, HostID: hostID, MachineID: machineID, GuestCID: guestCID,
		ProvisionLease: lease, ProvisionLeaseExpiresAt: leaseExpiresAt,
		Payload: runtimepostgres.PayloadPointer{Ref: provisionEvidence.Ref, Hash: provisionEvidence.Hash}, Actor: broker.Actor,
		CorrelationID: request.Execution.Payload.CorrelationID, ScratchRate: broker.ScratchRate,
	})
	if err != nil {
		return SandboxSession{}, err
	}
	if provisioned.Provision.SessionID != issued.SessionID {
		return SandboxSession{}, ErrSandboxAuthority
	}
	return SandboxSession{TenantID: issued.TenantID, SessionID: issued.SessionID, CapabilityToken: issued.CapabilityToken, ProvisionLease: lease, Host: host}, nil
}

func (broker PostgresSandboxBroker) selectPolicy(ctx context.Context, request SandboxAcquireRequest) (selectedRuntimePolicy, error) {
	binding := request.Snapshot.Descriptor.RuntimePolicy
	if binding == nil {
		return selectedRuntimePolicy{}, ErrSandboxAuthority
	}
	tx, err := broker.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return selectedRuntimePolicy{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, request.Execution.Claim.TenantID); err != nil {
		return selectedRuntimePolicy{}, err
	}
	var policy selectedRuntimePolicy
	err = tx.QueryRow(ctx, `SELECT id::text,snapshot_key,policy_hash,trust_tier,isolation_kind,network_mode,network_policy_hash,
		secret_mode,secret_scope_hash,workspace_mode,image_digest,kernel_digest,rootfs_digest,vcpu_count,memory_mib,disk_mib,pids_max,
		maximum_duration_seconds,approval_required FROM agent.runtime_policy_snapshots WHERE tenant_id=$1 AND snapshot_key=$2`,
		request.Execution.Claim.TenantID, binding.SnapshotKey).Scan(&policy.ID, &policy.SnapshotKey, &policy.PolicyHash, &policy.TrustTier,
		&policy.IsolationKind, &policy.NetworkMode, &policy.NetworkPolicyHash, &policy.SecretMode, &policy.SecretScopeHash,
		&policy.WorkspaceMode, &policy.ImageDigest, &policy.KernelDigest, &policy.RootFSDigest, &policy.VCPU, &policy.MemoryMiB,
		&policy.DiskMiB, &policy.Pids, &policy.MaximumSeconds, &policy.ApprovalRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return selectedRuntimePolicy{}, ErrSandboxAuthority
	}
	if err != nil {
		return selectedRuntimePolicy{}, err
	}
	if policy.ApprovalRequired {
		err = tx.QueryRow(ctx, `SELECT a.id::text,a.version FROM agent.tool_proposals p JOIN agent.approvals a
			ON a.tenant_id=p.tenant_id AND a.id=p.approval_id WHERE p.tenant_id=$1 AND p.tool_call_id=$2
			AND a.status='granted' AND a.tool_call_id=$2 AND a.run_id=$3 AND a.expires_at>$4`, request.Execution.Claim.TenantID,
			request.Execution.Claim.ToolCallID, request.Execution.Claim.RunID, broker.now()).Scan(&policy.ApprovalID, &policy.ApprovalVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return selectedRuntimePolicy{}, ErrSandboxAuthority
		}
		if err != nil {
			return selectedRuntimePolicy{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return selectedRuntimePolicy{}, err
	}
	if !runtimePolicyMatchesDescriptor(policy, request.Snapshot) {
		return selectedRuntimePolicy{}, ErrSandboxAuthority
	}
	return policy, nil
}

func (broker PostgresSandboxBroker) loadSession(ctx context.Context, issued sessionrequest.Issued) (persistedSandboxSession, error) {
	tx, err := broker.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return persistedSandboxSession{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, issued.TenantID); err != nil {
		return persistedSandboxSession{}, err
	}
	var value persistedSandboxSession
	var hostID, machineID *string
	var guestCID *int64
	var leaseExpires *time.Time
	err = tx.QueryRow(ctx, `SELECT status,host_id,machine_id,guest_cid,provision_lease_expires_at FROM agent.runtime_sessions WHERE tenant_id=$1 AND id=$2`, issued.TenantID, issued.SessionID).Scan(&value.Status, &hostID, &machineID, &guestCID, &leaseExpires)
	if err != nil {
		return persistedSandboxSession{}, err
	}
	if hostID != nil {
		value.HostID = *hostID
	}
	if machineID != nil {
		value.MachineID = *machineID
	}
	if guestCID != nil && *guestCID >= 0 && *guestCID <= math.MaxUint32 {
		value.GuestCID = uint32(*guestCID)
	}
	value.ProvisionLeaseExpiresAt = leaseExpires
	return value, tx.Commit(ctx)
}

func (broker PostgresSandboxBroker) place(ctx context.Context, policy selectedRuntimePolicy) (string, error) {
	rows, err := broker.PlacementPool.Query(ctx, `SELECT host_id FROM agent.runtime_hosts WHERE pool_key=$1 AND status='active' AND heartbeat_deadline>$2
		AND allocated_vcpu+$3<=capacity_vcpu AND allocated_memory_mib+$4<=capacity_memory_mib
		AND allocated_disk_mib+$5<=capacity_disk_mib AND allocated_sessions+1<=capacity_sessions
		ORDER BY (allocated_sessions::numeric/capacity_sessions),allocated_sessions,host_id LIMIT 64`, runtimePool(policy.TrustTier), broker.now(), policy.VCPU, policy.MemoryMiB, policy.DiskMiB)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var hostID string
		if rows.Scan(&hostID) != nil {
			continue
		}
		endpoint, ok := broker.Endpoints[hostID]
		if ok && endpoint.KernelDigest == policy.KernelDigest && endpoint.RootFSDigest == policy.RootFSDigest {
			return hostID, nil
		}
	}
	if rows.Err() != nil {
		return "", rows.Err()
	}
	return "", ErrSandboxCapacity
}

func (broker PostgresSandboxBroker) host(hostID string, policy selectedRuntimePolicy) (*runtimeclient.Client, error) {
	endpoint, ok := broker.Endpoints[hostID]
	if !ok || endpoint.URL == "" || endpoint.KernelDigest != policy.KernelDigest || endpoint.RootFSDigest != policy.RootFSDigest {
		return nil, ErrSandboxCapacity
	}
	return &runtimeclient.Client{BaseURL: endpoint.URL, HTTPClient: broker.HTTPClient, AllowInsecureDevelopment: broker.AllowInsecureDevelopment, MaximumResponseBytes: 24 << 20}, nil
}

func (broker PostgresSandboxBroker) putEvidence(ctx context.Context, tenantID, objectID, eventType string, data any) (payload.Manifest, error) {
	encoded, err := json.Marshal(struct {
		EventType string `json:"event_type"`
		Data      any    `json:"data"`
	}{eventType, data})
	if err != nil {
		return payload.Manifest{}, err
	}
	return broker.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: "event-payload", ContentType: "application/json"}, encoded)
}

func (broker PostgresSandboxBroker) provisionLease(sessionID string) string {
	mac := hmac.New(sha256.New, broker.ProvisionTokenDerivationKey)
	_, _ = mac.Write([]byte("runtime-provision-token-v1\x00" + sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (broker PostgresSandboxBroker) machineIdentity(sessionID, hostID string) (string, uint32) {
	mac := hmac.New(sha256.New, broker.MachineIdentityKey)
	_, _ = mac.Write([]byte("runtime-machine-v1\x00" + sessionID + "\x00" + hostID))
	digest := mac.Sum(nil)
	machineID := "lites-" + hex.EncodeToString(digest[:16])
	cid := binary.BigEndian.Uint32(digest[16:20])
	if cid < 3 {
		cid += 3
	}
	return machineID, cid
}

func (broker PostgresSandboxBroker) provisionExpiry(issued sessionrequest.Issued) time.Time {
	value := issued.RequestedAt.Add(55 * time.Second)
	for _, limit := range []time.Time{issued.ExecutionLeaseExpiresAt.Add(-time.Microsecond), issued.ExecutionDeadline.Add(-time.Microsecond)} {
		if limit.Before(value) {
			value = limit
		}
	}
	return value.UTC().Truncate(time.Microsecond)
}

func (broker PostgresSandboxBroker) valid() bool {
	var actor map[string]any
	if broker.Pool == nil || broker.PlacementPool == nil || nilInterface(broker.Issuer) || broker.Payloads == nil || broker.HTTPClient == nil || len(broker.Endpoints) == 0 || len(broker.ProvisionTokenPepper) < 32 || len(broker.ProvisionTokenDerivationKey) < 32 || len(broker.MachineIdentityKey) < 32 || json.Unmarshal(broker.Actor, &actor) != nil || actor == nil {
		return false
	}
	lease := broker.provisionLease("00000000-0000-0000-0000-000000000000")
	_, err := (opaque.Manager{Purpose: "runtime-provision-lease-v1", Pepper: broker.ProvisionTokenPepper}).Digest(lease)
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(lease)
	return err == nil && decodeErr == nil && len(decoded) == sha256.Size && validSandboxRate(broker.ScratchRate)
}

func (broker PostgresSandboxBroker) now() time.Time {
	if broker.Now != nil {
		return broker.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func runtimePolicyMatchesDescriptor(policy selectedRuntimePolicy, snapshot toolregistry.Snapshot) bool {
	descriptor, binding := snapshot.Descriptor, snapshot.Descriptor.RuntimePolicy
	timeout, err := time.ParseDuration(descriptor.Resources.Timeout)
	if err != nil || binding == nil {
		return false
	}
	imageAt := strings.LastIndex(descriptor.RuntimeImage, "@")
	return imageAt > 0 && policy.SnapshotKey == binding.SnapshotKey && policy.TrustTier == descriptor.TrustTier && policy.IsolationKind == binding.IsolationKind &&
		policy.NetworkMode == binding.NetworkMode && policy.NetworkPolicyHash == binding.NetworkPolicyHash && policy.SecretMode == binding.SecretMode && policy.SecretScopeHash == binding.SecretScopeHash &&
		policy.WorkspaceMode == binding.WorkspaceMode && policy.ImageDigest == descriptor.RuntimeImage[imageAt+1:] && policy.VCPU >= minimumVCPU(descriptor.Resources.CPUMillis) &&
		policy.MemoryMiB >= bytesToMiBRoundUp(descriptor.Resources.MemoryBytes) && policy.DiskMiB >= bytesToMiBRoundUp(descriptor.Resources.DiskBytes) && policy.Pids >= binding.MinimumPids &&
		policy.MaximumSeconds > 0 && time.Duration(policy.MaximumSeconds)*time.Second >= timeout && policy.ApprovalRequired == (descriptor.ApprovalMode != toolregistry.ApprovalNone)
}

func validSandboxAcquireRequest(request SandboxAcquireRequest) bool {
	return request.Execution.Claim.TenantID != "" && request.Execution.Claim.AttemptID != "" && request.Execution.Claim.ToolCallID != "" && request.Execution.Claim.RunID != "" && request.Execution.Payload.CorrelationID != "" && request.RequestID != "" && request.Snapshot.Descriptor.RuntimePolicy != nil
}

func minimumVCPU(cpuMillis int64) int { return max(1, int((cpuMillis+999)/1000)) }
func bytesToMiBRoundUp(value int64) int {
	if value <= 0 {
		return 0
	}
	return int((value + (1 << 20) - 1) >> 20)
}
func runtimePool(trustTier string) string {
	return "runtime-" + strings.ReplaceAll(trustTier, "_", "-")
}

func validSandboxRate(value firecracker.RateLimit) bool {
	valid := func(bucket firecracker.TokenBucket) bool {
		return bucket.Size > 0 && bucket.RefillMillis > 0 && bucket.RefillMillis <= 60_000 && bucket.Burst >= bucket.Size
	}
	return valid(value.Bandwidth) && valid(value.Operations)
}
