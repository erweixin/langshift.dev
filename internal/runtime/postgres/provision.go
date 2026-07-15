package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	runtimecontract "github.com/langshift/lites/internal/runtime"
)

type ProvisionCommand struct {
	CapabilityToken         string
	HostID                  string
	MachineID               string
	GuestCID                uint32
	ProvisionLease          string
	ProvisionLeaseExpiresAt time.Time
	Payload                 PayloadPointer
	Actor                   json.RawMessage
	CorrelationID           string
}

type sessionRow struct {
	ID, TenantID, UserID, RunID, ToolCallID                        string
	PolicySnapshotKey, PolicyHash                                  string
	WorkspaceID, BaseWorkspaceRevision                             sql.NullString
	WorkspaceMode, NetworkPolicyHash, SecretScopeHash, RequestHash string
	CapabilityNonceHash, ExecutionLeaseHash                        []byte
	CommandID, ExecutionAttemptID                                  string
	ExecutionFence, Version                                        uint64
	Status                                                         string
	ExecutionLeaseExpiresAt, RequestedAt, ExecutionDeadline        time.Time
	HostID, MachineID                                              sql.NullString
	GuestCID                                                       sql.NullInt64
	ProvisionAttemptID                                             sql.NullString
	ProvisionFence                                                 uint64
	ProvisionLeaseHash                                             []byte
	ProvisionLeaseExpiresAt                                        sql.NullTime
}

func (store Store) BeginProvision(ctx context.Context, command ProvisionCommand) (ProvisionResult, error) {
	if !store.valid() || command.HostID == "" || command.MachineID == "" || command.GuestCID < 3 || command.ProvisionLeaseExpiresAt.IsZero() || !validPayload(command.Payload) || !validActor(command.Actor) || command.CorrelationID == "" {
		return ProvisionResult{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return ProvisionResult{}, err
	}
	now := store.now()
	claims, err := store.Verifier.Verify(command.CapabilityToken, now)
	if err != nil {
		return ProvisionResult{}, err
	}
	provisionDigest, err := store.provisionTokens().Digest(command.ProvisionLease)
	if err != nil {
		return ProvisionResult{}, ErrInvalidCommand
	}
	leaseExpiresAt := command.ProvisionLeaseExpiresAt.UTC().Truncate(time.Microsecond)
	if !leaseExpiresAt.After(now) || leaseExpiresAt.After(now.Add(time.Minute)) {
		return ProvisionResult{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProvisionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claims.TenantID); err != nil {
		return ProvisionResult{}, err
	}
	session, policy, err := loadSessionForProvision(ctx, tx, claims, store.StoreEpoch)
	if err != nil {
		return ProvisionResult{}, err
	}
	if err = validateCapabilityBinding(claims, session, policy, now); err != nil {
		return ProvisionResult{}, err
	}
	allocationID, err := store.deterministicID("runtime-allocation", session.ID, 1)
	if err != nil {
		return ProvisionResult{}, err
	}
	provisionAttemptID, err := store.deterministicID("runtime-provision-attempt", session.ID, 1)
	if err != nil {
		return ProvisionResult{}, err
	}
	result := ProvisionResult{SessionID: session.ID, AllocationID: allocationID, ProvisionAttemptID: provisionAttemptID, MachineID: command.MachineID, GuestCID: command.GuestCID, Version: 2, Policy: policy}
	if session.Status != "requested" {
		if exactProvisionReplay(session, command, provisionAttemptID, provisionDigest[:], leaseExpiresAt) {
			result.Replayed = true
			return result, tx.Commit(ctx)
		}
		return ProvisionResult{}, ErrSessionConflict
	}
	if !leaseExpiresAt.Before(session.ExecutionLeaseExpiresAt) || !leaseExpiresAt.Before(session.ExecutionDeadline) {
		return ProvisionResult{}, ErrInvalidCommand
	}
	var executionRight string
	if err = tx.QueryRow(ctx, `SELECT agent.runtime_lock_execution_right($1,$2,NULLIF($3,'')::uuid,NULLIF($4,0),$5)`, session.TenantID, session.ID, claims.ApprovalID, claims.ApprovalVersion, now).Scan(&executionRight); err != nil {
		return ProvisionResult{}, err
	}
	switch executionRight {
	case "approval_invalid":
		return ProvisionResult{}, ErrApproval
	case "execution_invalid":
		return ProvisionResult{}, ErrCapabilityBinding
	case "valid":
	default:
		return ProvisionResult{}, ErrConfiguration
	}
	var hostPool string
	if err = tx.QueryRow(ctx, `SELECT pool_key FROM agent.runtime_hosts WHERE host_id=$1`, command.HostID).Scan(&hostPool); err != nil {
		return ProvisionResult{}, err
	}
	if hostPool != requiredPool(policy.TrustTier) || policy.IsolationKind != "firecracker" {
		return ProvisionResult{}, ErrHostPool
	}
	eventID, err := store.deterministicID("runtime-provision-event", session.ID, 2)
	if err != nil {
		return ProvisionResult{}, err
	}
	outboxID, err := store.deterministicID("runtime-provision-outbox", session.ID, 2)
	if err != nil {
		return ProvisionResult{}, err
	}
	publishID, err := store.deterministicID("runtime-provision-publish", session.ID, 2)
	if err != nil {
		return ProvisionResult{}, err
	}
	causation := claims.CommandID
	_, err = store.Appender.Append(ctx, tx, eventpostgres.Input{
		Event: eventpostgres.Event{
			ID: eventID, TenantID: session.TenantID, UserID: session.UserID, EventType: "RuntimeSessionProvisioningStarted", SchemaVersion: 1,
			AggregateKind: "runtime_session", AggregateID: session.ID, AggregateVersion: 2, StoreEpoch: store.StoreEpoch,
			OccurredAt: now, Actor: command.Actor, CausationID: &causation, CorrelationID: command.CorrelationID,
			PayloadRef: command.Payload.Ref, PayloadHash: command.Payload.Hash,
		},
		Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: command.Payload.Ref, PayloadHash: command.Payload.Hash}},
	})
	if err != nil {
		return ProvisionResult{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.runtime_sessions SET
	  version=2,status='provisioning',host_id=$1,machine_id=$2,guest_cid=$3,
	  provision_attempt_id=$4,provision_fence=1,provision_lease_hash=$5,provision_lease_expires_at=$6,
	  provisioning_at=$7,last_event_id=$8,updated_at=$7
	WHERE tenant_id=$9 AND id=$10 AND version=1 AND status='requested'`, command.HostID, command.MachineID, command.GuestCID, provisionAttemptID, provisionDigest[:], leaseExpiresAt, now, eventID, session.TenantID, session.ID)
	if err != nil {
		return ProvisionResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return ProvisionResult{}, ErrSessionConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.runtime_allocations(
	  id,tenant_id,user_id,session_id,host_id,machine_id,guest_cid,version,status,session_version,session_event_id,
	  provision_attempt_id,provision_fence,vcpu_count,memory_mib,disk_mib,lease_token_hash,lease_expires_at,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,1,'provisioning',2,$8,$9,1,$10,$11,$12,$13,$14,$15,$15)`, allocationID, session.TenantID, session.UserID, session.ID, command.HostID, command.MachineID, command.GuestCID, eventID, provisionAttemptID, policy.VCPUCount, policy.MemoryMiB, policy.DiskMiB, provisionDigest[:], leaseExpiresAt, now); err != nil {
		return ProvisionResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProvisionResult{}, err
	}
	return result, nil
}

func loadSessionForProvision(ctx context.Context, tx pgx.Tx, claims runtimecontract.CapabilityClaims, storeEpoch string) (sessionRow, RuntimePolicy, error) {
	var session sessionRow
	var policy RuntimePolicy
	var maximumSeconds, idleSeconds, graceSeconds int
	nonceDigest, err := runtimecontract.CapabilityNonceDigest(claims.Nonce)
	if err != nil {
		return sessionRow{}, RuntimePolicy{}, ErrCapabilityBinding
	}
	err = tx.QueryRow(ctx, `SELECT
	  s.id::text,s.tenant_id::text,s.user_id::text,s.run_id::text,s.tool_call_id::text,s.version,s.status,
	  s.policy_snapshot_key,s.policy_hash,s.workspace_id::text,s.base_workspace_revision,s.workspace_mode,
	  s.network_policy_hash,s.secret_scope_hash,s.request_hash,s.capability_nonce_hash,
	  s.command_id::text,s.execution_attempt_id::text,s.execution_fence,s.execution_lease_hash,
	  s.execution_lease_expires_at,s.requested_at,s.execution_deadline,s.host_id,s.machine_id,s.guest_cid,
	  s.provision_attempt_id::text,s.provision_fence,s.provision_lease_hash,s.provision_lease_expires_at,
	  p.id::text,p.snapshot_key,p.policy_hash,p.trust_tier,p.isolation_kind,p.network_policy_hash,p.secret_scope_hash,
	  p.workspace_mode,p.image_digest,p.kernel_digest,p.rootfs_digest,p.vcpu_count,p.memory_mib,p.disk_mib,p.pids_max,
	  p.maximum_duration_seconds,p.idle_timeout_seconds,p.kill_grace_seconds,p.approval_required
	FROM agent.runtime_sessions s JOIN agent.runtime_policy_snapshots p
	  ON p.tenant_id=s.tenant_id AND p.id=s.policy_snapshot_id
	JOIN agent.events le ON le.tenant_id=s.tenant_id AND le.id=s.last_event_id AND le.store_epoch=$3
	WHERE s.tenant_id=$1 AND s.capability_nonce_hash=$2 FOR UPDATE OF s`, claims.TenantID, nonceDigest[:], storeEpoch).Scan(
		&session.ID, &session.TenantID, &session.UserID, &session.RunID, &session.ToolCallID, &session.Version, &session.Status,
		&session.PolicySnapshotKey, &session.PolicyHash, &session.WorkspaceID, &session.BaseWorkspaceRevision, &session.WorkspaceMode,
		&session.NetworkPolicyHash, &session.SecretScopeHash, &session.RequestHash, &session.CapabilityNonceHash,
		&session.CommandID, &session.ExecutionAttemptID, &session.ExecutionFence, &session.ExecutionLeaseHash,
		&session.ExecutionLeaseExpiresAt, &session.RequestedAt, &session.ExecutionDeadline, &session.HostID, &session.MachineID, &session.GuestCID,
		&session.ProvisionAttemptID, &session.ProvisionFence, &session.ProvisionLeaseHash, &session.ProvisionLeaseExpiresAt,
		&policy.ID, &policy.SnapshotKey, &policy.PolicyHash, &policy.TrustTier, &policy.IsolationKind, &policy.NetworkPolicyHash, &policy.SecretScopeHash,
		&policy.WorkspaceMode, &policy.ImageDigest, &policy.KernelDigest, &policy.RootFSDigest, &policy.VCPUCount, &policy.MemoryMiB, &policy.DiskMiB, &policy.PidsMax,
		&maximumSeconds, &idleSeconds, &graceSeconds, &policy.ApprovalRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionRow{}, RuntimePolicy{}, ErrCapabilityBinding
	}
	policy.MaximumDuration, policy.IdleTimeout, policy.KillGrace = time.Duration(maximumSeconds)*time.Second, time.Duration(idleSeconds)*time.Second, time.Duration(graceSeconds)*time.Second
	return session, policy, err
}

func validateCapabilityBinding(claims runtimecontract.CapabilityClaims, session sessionRow, policy RuntimePolicy, now time.Time) error {
	leaseHash, err := decodeLeaseHash(claims.LeaseTokenHash)
	if err != nil {
		return err
	}
	nonceHash, err := runtimecontract.CapabilityNonceDigest(claims.Nonce)
	if err != nil {
		return ErrCapabilityBinding
	}
	workspaceID, baseRevision := "", ""
	if session.WorkspaceID.Valid {
		workspaceID = session.WorkspaceID.String
	}
	if session.BaseWorkspaceRevision.Valid {
		baseRevision = session.BaseWorkspaceRevision.String
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0).UTC()
	issuedAt := time.Unix(claims.IssuedAt, 0).UTC()
	if claims.TenantID != session.TenantID || claims.UserID != session.UserID || claims.RunID != session.RunID || claims.ToolCallID != session.ToolCallID ||
		claims.CommandID != session.CommandID || claims.AttemptID != session.ExecutionAttemptID || claims.Fence != session.ExecutionFence ||
		claims.PolicySnapshotID != session.PolicySnapshotKey || claims.PolicyHash != session.PolicyHash ||
		string(claims.WorkspaceMode) != session.WorkspaceMode || claims.WorkspaceID != workspaceID || claims.BaseWorkspaceRevision != baseRevision ||
		claims.NetworkPolicyHash != session.NetworkPolicyHash || claims.SecretScopeHash != session.SecretScopeHash || claims.RequestHash != session.RequestHash ||
		!bytes.Equal(leaseHash, session.ExecutionLeaseHash) || !bytes.Equal(nonceHash[:], session.CapabilityNonceHash) ||
		expiresAt.After(session.ExecutionLeaseExpiresAt) || expiresAt.After(session.ExecutionDeadline) || issuedAt.Add(time.Second).Before(session.RequestedAt) ||
		now.Before(issuedAt.Add(-30*time.Second)) || !now.Before(expiresAt) ||
		policy.SnapshotKey != session.PolicySnapshotKey || policy.PolicyHash != session.PolicyHash || policy.WorkspaceMode != session.WorkspaceMode ||
		policy.NetworkPolicyHash != session.NetworkPolicyHash || policy.SecretScopeHash != session.SecretScopeHash {
		return ErrCapabilityBinding
	}
	return nil
}

func exactProvisionReplay(session sessionRow, command ProvisionCommand, attemptID string, leaseHash []byte, leaseExpires time.Time) bool {
	return session.Status == "provisioning" && session.Version == 2 && session.HostID.Valid && session.HostID.String == command.HostID &&
		session.MachineID.Valid && session.MachineID.String == command.MachineID && session.GuestCID.Valid && uint32(session.GuestCID.Int64) == command.GuestCID &&
		session.ProvisionAttemptID.Valid && session.ProvisionAttemptID.String == attemptID && session.ProvisionFence == 1 &&
		bytes.Equal(session.ProvisionLeaseHash, leaseHash) && session.ProvisionLeaseExpiresAt.Valid && session.ProvisionLeaseExpiresAt.Time.Equal(leaseExpires)
}

func requiredPool(trustTier string) string {
	switch trustTier {
	case "semi_trusted":
		return "runtime-semi-trusted"
	case "untrusted":
		return "runtime-untrusted"
	case "privileged":
		return "runtime-privileged"
	default:
		return ""
	}
}
